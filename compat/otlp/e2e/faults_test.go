package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTraceBackendFailureAndRecovery(t *testing.T) {
	receiver := newReceiver(t)
	var recovered atomic.Bool
	receiver.SetHandler(func(w http.ResponseWriter, r *http.Request) {
		if !recovered.Load() {
			http.Error(w, "temporary unavailability", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	app := startApp(t, fixtureConfig{TraceEndpoint: receiver.URL + "/v1/traces"})
	getBehavior(t, app.URL+"/ok/before")
	failed := nextBehaviorExport(t, receiver.Requests)
	if len(behaviorSpans(t, failed)) != 1 {
		t.Fatal("failed batch missing request span")
	}
	awaitBehaviorStats(t, app, func(stats behaviorStats) bool { return stats.Trace.FailedExports > 0 })
	recovered.Store(true)
	getBehavior(t, app.URL+"/ok/after")
	if err := app.StopError(t); err == nil {
		t.Fatal("shutdown did not report historical export failure")
	}
	recoveredRequest := nextBehaviorExport(t, receiver.Requests)
	if len(behaviorSpans(t, recoveredRequest)) != 1 {
		t.Fatal("recovery batch missing request span")
	}
}

func TestBlockedExporterDoesNotBlockRequests(t *testing.T) {
	receiver := newReceiver(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	receiver.SetHandler(func(w http.ResponseWriter, r *http.Request) {
		enteredOnce.Do(func() { close(entered) })
		select {
		case <-release:
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	})
	app := startApp(t, fixtureConfig{TraceEndpoint: receiver.URL + "/v1/traces"})
	getBehavior(t, app.URL+"/ok/before-block")
	select {
	case <-entered:
	case <-time.After(12 * time.Second):
		t.Fatal("exporter did not reach backend")
	}
	req, err := http.NewRequest(http.MethodGet, app.URL+"/ok/while-blocked", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("application request blocked by exporter: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("application response: %d", response.StatusCode)
	}
	stats := readBehaviorStats(t, app)
	if stats.Trace.FailedExports != 0 || stats.Trace.DroppedSpans != 0 {
		t.Fatalf("unexpected loss while blocked: %+v", stats.Trace)
	}
	unblock()
	app.Stop(t)
	total := 0
	for total < 2 {
		total += len(behaviorSpans(t, nextBehaviorExport(t, receiver.Requests)))
	}
	if total != 2 {
		t.Fatalf("exported %d spans, want 2", total)
	}
}

func TestMetricSeriesLimitReportsDrops(t *testing.T) {
	app := startApp(t, fixtureConfig{MetricSeriesLimit: 1})
	getBehavior(t, app.URL+"/ok/first")
	getBehavior(t, app.URL+"/fail")
	stats := readBehaviorStats(t, app)
	if stats.Metrics.DroppedSeries == 0 {
		t.Fatalf("series limit did not report drops: %+v", stats)
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(app.MetricsURL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	assertBehaviorMetric(t, string(raw), "http_request_count", "/ok/{id}", 1)
	if _, count := behaviorMetric(string(raw), "http_request_count", "/fail"); count != 0 {
		t.Fatalf("rejected series exposed: %s", raw)
	}
}

type behaviorStats struct {
	Trace   struct{ DroppedSpans, FailedExports uint64 } `json:"trace"`
	Metrics struct{ DroppedSeries uint64 }               `json:"metrics"`
}

func readBehaviorStats(t *testing.T, app *fixtureApp) behaviorStats {
	t.Helper()
	response, err := (&http.Client{Timeout: 2 * time.Second}).Get(app.ControlURL + "/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stats status: %d", response.StatusCode)
	}
	var stats behaviorStats
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&stats); err != nil {
		t.Fatal(err)
	}
	return stats
}
func awaitBehaviorStats(t *testing.T, app *fixtureApp, predicate func(behaviorStats) bool) behaviorStats {
	t.Helper()
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		stats := readBehaviorStats(t, app)
		if predicate(stats) {
			return stats
		}
		select {
		case <-deadline.C:
			t.Fatalf("stats predicate deadline: %+v", stats)
		case <-poll.C:
		}
	}
}

func TestHandlerPanicRecordsFailure(t *testing.T) {
	receiver := newReceiver(t)
	app := startApp(t, fixtureConfig{TraceEndpoint: receiver.URL + "/v1/traces"})
	response, err := (&http.Client{Timeout: 2 * time.Second}).Get(app.URL + "/panic")
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("panicking handler unexpectedly returned a response")
	}
	app.Stop(t)
	spans := behaviorSpans(t, nextBehaviorExport(t, receiver.Requests))
	if len(spans) != 1 || behaviorStringAttr(spans[0], "http.path") != "/panic" || spans[0].GetStatus().GetCode() != 2 {
		t.Fatalf("panic span: %v", spans)
	}
}
