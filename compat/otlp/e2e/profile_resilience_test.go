package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProfileTransientRetryIdentity(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			receiver := newReceiver(t)
			var attempts atomic.Uint64
			receiver.SetHandler(func(w http.ResponseWriter, _ *http.Request) {
				if attempts.Add(1) == 1 {
					w.WriteHeader(status)
					return
				}
				w.WriteHeader(http.StatusOK)
			})
			app := startApp(t, fixtureConfig{ProfileEndpoint: receiver.URL + "/v1/profiles", ProfileTypes: []string{"goroutine"}, ProfileIntervalMillis: 2000, ProfileRetries: 2})
			first := nextBehaviorExport(t, receiver.Requests)
			second := nextBehaviorExport(t, receiver.Requests)
			assertResilienceProfile(t, first, "goroutine")
			assertResilienceProfile(t, second, "goroutine")
			if !bytes.Equal(first.Body, second.Body) {
				t.Fatal("retry changed profile payload or identity")
			}
			stats := awaitResilienceStats(t, app, func(s resilienceProfileStats) bool { return s.Exported == 1 })
			if stats.Failed != 0 || stats.Skipped != 0 {
				t.Fatalf("successful retry counters: %+v", stats)
			}
			app.Stop(t)
			assertResilienceFinalStats(t, app, resilienceProfileStats{Exported: 1})
			if got := attempts.Load(); got != 2 {
				t.Fatalf("attempts = %d, want 2", got)
			}
			assertResilienceNoExport(t, receiver)
		})
	}
}

func TestProfileTerminalAndExhaustedRetries(t *testing.T) {
	for _, tc := range []struct {
		status, retries, attempts int
	}{{400, 3, 1}, {401, 3, 1}, {403, 3, 1}, {503, 0, 1}, {503, 2, 3}, {429, 3, 4}} {
		t.Run(fmt.Sprintf("status%d_retries%d", tc.status, tc.retries), func(t *testing.T) {
			receiver := newReceiver(t)
			var attempts atomic.Uint64
			receiver.SetHandler(func(w http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				w.WriteHeader(tc.status)
			})
			app := startApp(t, fixtureConfig{ProfileEndpoint: receiver.URL + "/v1/profiles", ProfileTypes: []string{"goroutine"}, ProfileIntervalMillis: 2000, ProfileRetries: tc.retries})
			var first []byte
			for range tc.attempts {
				export := nextBehaviorExport(t, receiver.Requests)
				assertResilienceProfile(t, export, "goroutine")
				if first == nil {
					first = export.Body
				} else if !bytes.Equal(first, export.Body) {
					t.Fatal("exhausted retry changed collection payload")
				}
			}
			stats := awaitResilienceStats(t, app, func(s resilienceProfileStats) bool { return s.Failed == 1 })
			if stats.Exported != 0 || stats.Skipped != 0 {
				t.Fatalf("failed collection counters: %+v", stats)
			}
			app.Stop(t)
			assertResilienceFinalStats(t, app, resilienceProfileStats{Failed: 1})
			if got := attempts.Load(); got != uint64(tc.attempts) {
				t.Fatalf("attempts = %d, want %d", got, tc.attempts)
			}
			assertResilienceNoExport(t, receiver)
		})
	}
}

func TestProfileTimeoutCancelsStalledBackend(t *testing.T) {
	receiver := newReceiver(t)
	entered, canceled, release := stalledProfileReceiver(receiver)
	defer close(release)
	app := startApp(t, fixtureConfig{ProfileEndpoint: receiver.URL + "/v1/profiles", ProfileTypes: []string{"goroutine"}, ProfileIntervalMillis: 2000, ProfileTimeoutMillis: 300, ProfileRetries: 3})
	awaitResilienceSignal(t, entered, time.Second*5, "backend entry")
	assertResilienceProfile(t, nextBehaviorExport(t, receiver.Requests), "goroutine")
	resilienceResponsiveGET(t, app.URL+"/ok/during-profile-stall")
	awaitResilienceSignal(t, canceled, time.Second, "profile timeout cancellation")
	stats := awaitResilienceStats(t, app, func(s resilienceProfileStats) bool { return s.Failed == 1 })
	if stats.Exported != 0 || stats.Skipped != 0 {
		t.Fatalf("timeout counters: %+v", stats)
	}
	profileGET(t, app.URL+"/ok/after-profile-timeout", http.StatusNoContent)
	app.Stop(t)
	assertResilienceFinalStats(t, app, resilienceProfileStats{Failed: 1})
	assertResilienceNoExport(t, receiver)
}

func TestProfileShutdownCancelsInflightUpload(t *testing.T) {
	receiver := newReceiver(t)
	entered, canceled, release := stalledProfileReceiver(receiver)
	defer close(release)
	app := startApp(t, fixtureConfig{ProfileEndpoint: receiver.URL + "/v1/profiles", ProfileTypes: []string{"goroutine"}, ProfileIntervalMillis: 2000, ProfileTimeoutMillis: 10000, ProfileRetries: 3, ShutdownMillis: 1000})
	awaitResilienceSignal(t, entered, 5*time.Second, "backend entry")
	assertResilienceProfile(t, nextBehaviorExport(t, receiver.Requests), "goroutine")
	if stats := readResilienceStats(t, app); stats != (resilienceProfileStats{}) {
		t.Fatalf("inflight upload counters: %+v", stats)
	}
	profileGET(t, app.ControlURL+"/shutdown", http.StatusNoContent)
	awaitResilienceSignal(t, canceled, time.Second, "shutdown upload cancellation")
	app.Stop(t)
	assertResilienceFinalStats(t, app, resilienceProfileStats{Failed: 1})
	assertResilienceNoExport(t, receiver)
}

func TestProfileCollectionByteLimit(t *testing.T) {
	receiver := newReceiver(t)
	app := startApp(t, fixtureConfig{ProfileEndpoint: receiver.URL + "/v1/profiles", ProfileTypes: []string{"goroutine"}, ProfileIntervalMillis: 2000, ProfileMaxBytes: 1, ProfileRetries: 3})
	stats := awaitResilienceStats(t, app, func(s resilienceProfileStats) bool { return s.Failed == 1 })
	if stats.Exported != 0 || stats.Skipped != 0 {
		t.Fatalf("byte limit counters: %+v", stats)
	}
	app.Stop(t)
	assertResilienceFinalStats(t, app, resilienceProfileStats{Failed: 1})
	assertResilienceNoExport(t, receiver)
}

func TestProfileBusyCPUOwnerAndRecovery(t *testing.T) {
	receiver := newReceiver(t)
	app := startApp(t, fixtureConfig{ProfileEndpoint: receiver.URL + "/v1/profiles", ProfileTypes: []string{"cpu"}, ProfileIntervalMillis: 1000, CPUWindowMillis: 300})
	resiliencePOST(t, app.ControlURL+"/cpu-profile/start", http.StatusNoContent)
	profileGET(t, app.ControlURL+"/workload", http.StatusNoContent)
	stats := awaitResilienceStats(t, app, func(s resilienceProfileStats) bool { return s.Skipped >= 2 })
	if stats.Exported != 0 || stats.Failed != 0 {
		t.Fatalf("busy CPU counters: %+v", stats)
	}
	assertResilienceNoExport(t, receiver)
	resiliencePOST(t, app.ControlURL+"/cpu-profile/stop", http.StatusNoContent)
	assertResilienceProfile(t, nextBehaviorExport(t, receiver.Requests), "cpu")
	stats = awaitResilienceStats(t, app, func(s resilienceProfileStats) bool { return s.Exported > 0 })
	if stats.Failed != 0 || stats.Skipped < 2 {
		t.Fatalf("CPU recovery counters: %+v", stats)
	}
	app.Stop(t)
	assertResilienceFinalStats(t, app, stats)
	assertResilienceNoExport(t, receiver)
}

func TestProfileStopBeforeFirstTick(t *testing.T) {
	receiver := newReceiver(t)
	app := startApp(t, fixtureConfig{ProfileEndpoint: receiver.URL + "/v1/profiles", ProfileTypes: []string{"goroutine"}, ProfileIntervalMillis: 60000})
	if stats := readResilienceStats(t, app); stats != (resilienceProfileStats{}) {
		t.Fatalf("pre-tick counters: %+v", stats)
	}
	app.Stop(t)
	assertResilienceFinalStats(t, app, resilienceProfileStats{})
	assertResilienceNoExport(t, receiver)
}

type resilienceProfileStats struct{ Exported, Failed, Skipped uint64 }

func readResilienceStats(t *testing.T, app *fixtureApp) resilienceProfileStats {
	t.Helper()
	var stats struct{ Profiles resilienceProfileStats }
	if err := json.Unmarshal(profileGET(t, app.ControlURL+"/stats", http.StatusOK), &stats); err != nil {
		t.Fatal(err)
	}
	return stats.Profiles
}

func awaitResilienceStats(t *testing.T, app *fixtureApp, ready func(resilienceProfileStats) bool) resilienceProfileStats {
	t.Helper()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		stats := readResilienceStats(t, app)
		if ready(stats) {
			return stats
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatalf("profile counters deadline: %+v\n%s", stats, app.Logs())
		}
	}
}

func assertResilienceProfile(t *testing.T, export exportRequest, kind string) {
	t.Helper()
	request := decodeProfileExport(t, export)
	count := 0
	for _, resource := range request.ResourceProfiles {
		for _, scope := range resource.ScopeProfiles {
			for _, profile := range scope.Profiles {
				if got := profileKind(t, request.Dictionary, profile); got != kind {
					t.Fatalf("profile kind = %q, want %q", got, kind)
				}
				validateNativeProfile(t, request.Dictionary, profile, kind)
				count++
			}
		}
	}
	if count == 0 {
		t.Fatal("missing native profile")
	}
}

func assertResilienceNoExport(t *testing.T, receiver *receiver) {
	t.Helper()
	select {
	case export := <-receiver.Requests:
		t.Fatalf("unexpected profile delivery: %s (%d bytes)", export.Path, len(export.Body))
	default:
	}
}

func stalledProfileReceiver(receiver *receiver) (<-chan struct{}, <-chan struct{}, chan struct{}) {
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var enterOnce, cancelOnce sync.Once
	receiver.SetHandler(func(_ http.ResponseWriter, r *http.Request) {
		enterOnce.Do(func() { close(entered) })
		select {
		case <-r.Context().Done():
			cancelOnce.Do(func() { close(canceled) })
		case <-release:
		}
	})
	return entered, canceled, release
}

func awaitResilienceSignal(t *testing.T, signal <-chan struct{}, timeout time.Duration, message string) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-signal:
	case <-timer.C:
		t.Fatal(message + " deadline")
	}
}

func resiliencePOST(t *testing.T, url string, status int) {
	t.Helper()
	response, err := (&http.Client{Timeout: time.Second}).Post(url, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("POST %s: status %d, want %d: %s", url, response.StatusCode, status, body)
	}
}

func resilienceResponsiveGET(t *testing.T, url string) {
	t.Helper()
	response, err := (&http.Client{Timeout: 200 * time.Millisecond}).Get(url)
	if err != nil {
		t.Fatalf("application blocked by profile delivery: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("application response: %d", response.StatusCode)
	}
}

func assertResilienceFinalStats(t *testing.T, app *fixtureApp, want resilienceProfileStats) {
	t.Helper()
	final := app.FinalStats(t).Profiles
	got := resilienceProfileStats{Exported: final.Exported, Failed: final.Failed, Skipped: final.Skipped}
	if got != want {
		t.Fatalf("final profile counters: %+v, want %+v", got, want)
	}
}
