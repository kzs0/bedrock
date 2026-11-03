package bedrock

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace/otlp"
)

func TestProfileConfigBoundsAndDefensiveCopy(t *testing.T) {
	for _, endpoint := range []string{"", "https://", "https://user:pass@example.test/v1/profiles", "https://example.test/v1/profiles?q=x", "https://example.test/v1/profiles#fragment", "ftp://example.test/profiles", ":bad"} {
		if _, err := normalizeProfileConfig(ProfileExportConfig{Endpoint: endpoint}); err == nil {
			t.Errorf("unsafe endpoint accepted: %q", endpoint)
		}
	}
	for name, change := range map[string]func(*ProfileExportConfig){
		"negative timeout":        func(c *ProfileExportConfig) { c.Timeout = -1 },
		"negative interval":       func(c *ProfileExportConfig) { c.Interval = -1 },
		"negative CPU window":     func(c *ProfileExportConfig) { c.CPUSampleDuration = -1 },
		"negative bytes":          func(c *ProfileExportConfig) { c.MaxBytes = -1 },
		"excessive bytes":         func(c *ProfileExportConfig) { c.MaxBytes = (32 << 20) + 1 },
		"negative retries":        func(c *ProfileExportConfig) { c.Retries = -1 },
		"negative mutex sampling": func(c *ProfileExportConfig) { c.MutexSampleFraction = -1 },
		"negative block sampling": func(c *ProfileExportConfig) { c.BlockSampleRate = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := ProfileExportConfig{Endpoint: "https://example.test/profiles"}
			change(&cfg)
			if _, err := normalizeProfileConfig(cfg); err == nil {
				t.Fatal("invalid bound accepted")
			}
		})
	}
	input := ProfileExportConfig{Endpoint: "https://example.test/profiles", Headers: map[string]string{"X-Test": "original"}, TLSConfig: &tls.Config{ServerName: "original"}, Types: []ProfileType{ProfileTypeHeap}}
	cfg, err := normalizeProfileConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Headers["X-Test"], input.TLSConfig.ServerName, input.Types[0] = "changed", "changed", ProfileTypeCPU
	if cfg.Headers["X-Test"] != "original" || cfg.TLSConfig.ServerName != "original" || cfg.Types[0] != ProfileTypeHeap {
		t.Fatal("caller mutation changed normalized configuration")
	}
	if cfg.Timeout != 30*time.Second || cfg.Interval != time.Minute || cfg.CPUSampleDuration != 10*time.Second || cfg.MaxBytes != 8<<20 {
		t.Fatalf("defaults: %+v", cfg)
	}
}

func TestProfileWorkerAdmissionAndContention(t *testing.T) {
	b, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
	cfg := ProfileExportConfig{Endpoint: "https://example.test/profiles", Types: []ProfileType{}, Interval: time.Hour}
	for range 8 {
		stop, startErr := b.StartProfileExport(cfg)
		if startErr != nil {
			t.Fatal(startErr)
		}
		stop(nil)
		stop(context.Background())
	}
	if _, err := b.StartProfileExport(cfg); err == nil {
		t.Fatal("ninth worker admitted")
	}
	other, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Shutdown(context.Background()) })
	previous := runtime.SetMutexProfileFraction(3)
	defer runtime.SetMutexProfileFraction(previous)
	cfg.MutexSampleFraction = 2
	if _, err := other.StartProfileExport(cfg); err == nil {
		t.Fatal("conflicting mutex sampling replaced existing owner")
	}
	if current := runtime.SetMutexProfileFraction(-1); current != 3 {
		t.Fatalf("mutex owner changed to %d", current)
	}
}

func TestProfileDisabledContentionSamplingReportsSkips(t *testing.T) {
	previous := runtime.SetMutexProfileFraction(0)
	defer runtime.SetMutexProfileFraction(previous)
	b, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
	stop, err := b.StartProfileExport(ProfileExportConfig{Endpoint: "https://example.test/profiles", Types: []ProfileType{ProfileTypeMutex, ProfileTypeBlock}, Interval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for b.ProfileStats().Skipped < 2 {
		select {
		case <-deadline:
			t.Fatal("disabled contention collectors did not report skips")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	stop(context.Background())
	if stats := b.ProfileStats(); stats.Exported != 0 || stats.Failed != 0 || stats.Skipped < 2 {
		t.Fatalf("skip statistics: %+v", stats)
	}
}

type boundaryProfileTransport func(*http.Request) (*http.Response, error)

func (f boundaryProfileTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProfileCollectionRejectsBeforeDelivery(t *testing.T) {
	client := &http.Client{Transport: boundaryProfileTransport(func(*http.Request) (*http.Response, error) {
		t.Error("invalid collection reached transport")
		return nil, errors.New("unexpected delivery")
	})}
	for _, typ := range []ProfileType{ProfileTypeHeap, ProfileTypeGoroutine, ProfileTypeMutex, ProfileTypeBlock} {
		t.Run(string(typ), func(t *testing.T) {
			cfg := ProfileExportConfig{Endpoint: "http://unused.test", MaxBytes: 1, Timeout: time.Second}
			if err := collectAndPushProfileWithConfig(context.Background(), "svc", attr.EmptySet, cfg, client, typ, otlp.ProfileEncodingOptions{}); err == nil || !strings.Contains(err.Error(), "profile") {
				t.Fatalf("collection limit: %v", err)
			}
		})
	}
	for _, duration := range []time.Duration{0, -1} {
		cfg := ProfileExportConfig{CPUSampleDuration: duration}
		if err := collectAndPushProfileWithConfig(context.Background(), "svc", attr.EmptySet, cfg, client, ProfileTypeCPU, otlp.ProfileEncodingOptions{}); err == nil {
			t.Fatal("invalid CPU window accepted")
		}
	}
	cpuProfileSlot <- struct{}{}
	err := collectAndPushProfileWithConfig(context.Background(), "svc", attr.EmptySet, ProfileExportConfig{CPUSampleDuration: time.Millisecond}, client, ProfileTypeCPU, otlp.ProfileEncodingOptions{})
	<-cpuProfileSlot
	if !errors.Is(err, ErrCPUProfilerBusy) {
		t.Fatalf("owned CPU slot: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := collectAndPushProfileWithConfig(ctx, "svc", attr.EmptySet, ProfileExportConfig{}, client, ProfileTypeHeap, otlp.ProfileEncodingOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled collection: %v", err)
	}
	if err := collectAndPushProfileWithConfig(context.Background(), "svc", attr.EmptySet, ProfileExportConfig{Endpoint: "\n", Timeout: time.Second}, client, ProfileTypeGoroutine, otlp.ProfileEncodingOptions{}); err == nil || !strings.Contains(err.Error(), "profile request") {
		t.Fatalf("invalid request URL: %v", err)
	}
}

func TestProfileDeliveryFailuresAndRetryIdentity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		retries  int
		failure  error
		status   int
		recover  bool
		want     string
		attempts int
	}{
		{"network exhaustion", 2, errors.New("secret network detail"), 0, false, "HTTP delivery failed", 3},
		{"network recovery", 1, errors.New("temporary"), 0, true, "", 2},
		{"deadline", 0, context.DeadlineExceeded, 0, false, "context deadline exceeded", 1},
		{"server recovery", 1, nil, 503, true, "", 2},
		{"terminal status", 3, nil, 403, false, "status 403", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			var first []byte
			client := &http.Client{Transport: boundaryProfileTransport(func(r *http.Request) (*http.Response, error) {
				attempts++
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if first == nil {
					first = body
				} else if !bytes.Equal(first, body) {
					t.Error("retry changed payload identity")
				}
				if r.Header.Get("Content-Type") != "application/x-protobuf" || r.Header.Get("x-profile-type") != "goroutine" {
					t.Error("missing profile headers")
				}
				if tc.recover && attempts > 1 {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				if tc.failure != nil {
					return nil, tc.failure
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			cfg := ProfileExportConfig{Endpoint: "http://example.test/profiles", Timeout: time.Second, Retries: tc.retries}
			err := collectAndPushProfileWithConfig(context.Background(), "svc", attr.EmptySet, cfg, client, ProfileTypeGoroutine, otlp.ProfileEncodingOptions{})
			if (tc.want == "" && err != nil) || (tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want))) {
				t.Fatalf("delivery: %v, want %q", err, tc.want)
			}
			if attempts != tc.attempts || len(first) == 0 {
				t.Fatalf("attempts=%d, payload=%d", attempts, len(first))
			}
			if err != nil && strings.Contains(err.Error(), "secret network detail") {
				t.Fatal("transport error disclosed details")
			}
		})
	}
}
