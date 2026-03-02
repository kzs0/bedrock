package bedrock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kzs0/bedrock/metric"
	"github.com/kzs0/bedrock/trace"
)

// ── retryExporter ─────────────────────────────────────────────────────────────

type mockExporter struct {
	calls     atomic.Int32
	failUntil int32
	spans     [][]*trace.Span
}

func (m *mockExporter) ExportSpans(_ context.Context, spans []*trace.Span) error {
	n := m.calls.Add(1)
	if n <= m.failUntil {
		return errMockFailure
	}
	m.spans = append(m.spans, spans)
	return nil
}

func (m *mockExporter) Shutdown(_ context.Context) error { return nil }

var errMockFailure = &mockErr{"mock export failure"}

type mockErr struct{ msg string }

func (e *mockErr) Error() string { return e.msg }

func TestRetryExporter_SucceedsAfterRetries(t *testing.T) {
	mock := &mockExporter{failUntil: 2} // fail first 2 attempts, succeed on 3rd
	re := &retryExporter{base: mock, maxRetries: 3, initialDelay: time.Millisecond}

	err := re.ExportSpans(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if calls := mock.calls.Load(); calls != 3 {
		t.Errorf("expected 3 calls (2 failures + 1 success), got %d", calls)
	}
}

func TestRetryExporter_ExhaustsRetries(t *testing.T) {
	mock := &mockExporter{failUntil: 10} // always fail
	re := &retryExporter{base: mock, maxRetries: 3, initialDelay: time.Millisecond}

	err := re.ExportSpans(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if calls := mock.calls.Load(); calls != 4 {
		t.Errorf("expected 4 calls (1 + 3 retries), got %d", calls)
	}
}

func TestRetryExporter_SuccessOnFirstTry(t *testing.T) {
	mock := &mockExporter{failUntil: 0}
	re := &retryExporter{base: mock, maxRetries: 3, initialDelay: time.Millisecond}

	err := re.ExportSpans(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if calls := mock.calls.Load(); calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

// ── fanOutExporter ────────────────────────────────────────────────────────────

func TestFanOutExporter_CallsBoth(t *testing.T) {
	m1 := &mockExporter{}
	m2 := &mockExporter{}
	fo := &fanOutExporter{exporters: []trace.Exporter{m1, m2}}

	err := fo.ExportSpans(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m1.calls.Load() != 1 || m2.calls.Load() != 1 {
		t.Errorf("expected both exporters called once, got %d and %d", m1.calls.Load(), m2.calls.Load())
	}
}

func TestFanOutExporter_ReturnsFirstError(t *testing.T) {
	m1 := &mockExporter{failUntil: 99}
	m2 := &mockExporter{}
	fo := &fanOutExporter{exporters: []trace.Exporter{m1, m2}}

	err := fo.ExportSpans(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error from failing exporter")
	}
	// m2 should still be called even when m1 fails.
	if m2.calls.Load() != 1 {
		t.Errorf("expected m2 called once despite m1 failure, got %d", m2.calls.Load())
	}
}

// ── WithCloud API key propagation ─────────────────────────────────────────────

func TestWithCloud_APIKeyInMetricsHeader(t *testing.T) {
	var receivedKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedKey = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := cloudConfig{
		apiKey:              "brk_test_abc123",
		endpoint:            srv.URL,
		pushInterval:        100 * time.Millisecond,
		profileInterval:     999 * time.Hour, // effectively disabled
		profileCPUSampleDur: time.Millisecond,
	}

	// Trigger one immediate push.
	registry := metric.NewRegistry("")
	stop := startMetricsPush(registry, nil, cfg)

	// Wait for push.
	time.Sleep(200 * time.Millisecond)
	stop(context.Background())

	if receivedKey != "Bearer brk_test_abc123" {
		t.Errorf("expected 'Bearer brk_test_abc123', got %q", receivedKey)
	}
}

// ── cloudGRPCEndpoint ─────────────────────────────────────────────────────────

func TestCloudGRPCEndpoint_StripHTTPS(t *testing.T) {
	got := cloudGRPCEndpoint("https://ingest.bedrock.dev")
	want := "ingest.bedrock.dev:4317"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestCloudGRPCEndpoint_StripHTTP(t *testing.T) {
	got := cloudGRPCEndpoint("http://localhost:4317")
	want := "localhost:4317"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestCloudGRPCEndpoint_NoScheme(t *testing.T) {
	got := cloudGRPCEndpoint("collector:4317")
	want := "collector:4317"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestCloudGRPCEndpoint_AddDefaultPort(t *testing.T) {
	got := cloudGRPCEndpoint("https://collector.example.com")
	want := "collector.example.com:4317"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// ── CloudOption parsing ───────────────────────────────────────────────────────

func TestWithCloud_DefaultEndpoint(t *testing.T) {
	var captured *cloudConfig
	opt := WithCloud("key123")
	cfg := &initConfig{}
	opt(cfg)
	captured = cfg.cloudCfg

	if captured == nil {
		t.Fatal("cloudCfg should be set")
	}
	if captured.endpoint != defaultCloudEndpoint {
		t.Errorf("expected %q, got %q", defaultCloudEndpoint, captured.endpoint)
	}
	if captured.apiKey != "key123" {
		t.Errorf("expected 'key123', got %q", captured.apiKey)
	}
}

func TestWithCloud_CustomEndpoint(t *testing.T) {
	opt := WithCloud("key", CloudEndpoint("http://localhost:9999"))
	cfg := &initConfig{}
	opt(cfg)
	if cfg.cloudCfg.endpoint != "http://localhost:9999" {
		t.Errorf("unexpected endpoint: %q", cfg.cloudCfg.endpoint)
	}
}

func TestWithCloud_Insecure(t *testing.T) {
	opt := WithCloud("key", CloudInsecure())
	cfg := &initConfig{}
	opt(cfg)
	if !cfg.cloudCfg.insecure {
		t.Error("expected insecure=true")
	}
}

func TestWithCloud_CustomIntervals(t *testing.T) {
	opt := WithCloud("key",
		CloudPushInterval(30*time.Second),
		CloudProfileInterval(5*time.Minute),
		CloudProfileCPUSampleDuration(5*time.Second),
	)
	cfg := &initConfig{}
	opt(cfg)
	cc := cfg.cloudCfg
	if cc.pushInterval != 30*time.Second {
		t.Errorf("unexpected pushInterval: %v", cc.pushInterval)
	}
	if cc.profileInterval != 5*time.Minute {
		t.Errorf("unexpected profileInterval: %v", cc.profileInterval)
	}
	if cc.profileCPUSampleDur != 5*time.Second {
		t.Errorf("unexpected profileCPUSampleDur: %v", cc.profileCPUSampleDur)
	}
}

