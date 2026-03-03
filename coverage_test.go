package bedrock

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace"
)

// ── CounterWithStatic (api.go: Add) ─────────────────────────────────────────

func TestCounterWithStatic_Add(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	counter := Counter(ctx, "test_add_counter", "test")
	counter.Add(5)

	b := FromContext(ctx)
	families := b.Metrics().Gather()
	found := false
	for _, f := range families {
		if f.Name == "test_add_counter" {
			found = true
			if len(f.Metrics) > 0 && f.Metrics[0].Value != 5 {
				t.Errorf("expected value 5, got %f", f.Metrics[0].Value)
			}
		}
	}
	if !found {
		t.Error("counter not found")
	}
}

func TestCounterWithStatic_Inc(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	counter := Counter(ctx, "test_inc_counter", "test")
	counter.Inc()
	counter.Inc()

	b := FromContext(ctx)
	families := b.Metrics().Gather()
	for _, f := range families {
		if f.Name == "test_inc_counter" && len(f.Metrics) > 0 {
			if f.Metrics[0].Value != 2 {
				t.Errorf("expected value 2, got %f", f.Metrics[0].Value)
			}
		}
	}
}

// ── GaugeWithStatic (api.go: Inc, Dec, Add, Sub) ───────────────────────────

func TestGaugeWithStatic_Inc(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	gauge := Gauge(ctx, "test_gauge_inc", "test")
	gauge.Inc()
	gauge.Inc()

	b := FromContext(ctx)
	families := b.Metrics().Gather()
	for _, f := range families {
		if f.Name == "test_gauge_inc" && len(f.Metrics) > 0 {
			if f.Metrics[0].Value != 2 {
				t.Errorf("expected 2, got %f", f.Metrics[0].Value)
			}
		}
	}
}

func TestGaugeWithStatic_Dec(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	gauge := Gauge(ctx, "test_gauge_dec", "test")
	gauge.Set(10)
	gauge.Dec()

	b := FromContext(ctx)
	families := b.Metrics().Gather()
	for _, f := range families {
		if f.Name == "test_gauge_dec" && len(f.Metrics) > 0 {
			if f.Metrics[0].Value != 9 {
				t.Errorf("expected 9, got %f", f.Metrics[0].Value)
			}
		}
	}
}

func TestGaugeWithStatic_Add(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	gauge := Gauge(ctx, "test_gauge_add", "test")
	gauge.Add(5.5)

	b := FromContext(ctx)
	families := b.Metrics().Gather()
	for _, f := range families {
		if f.Name == "test_gauge_add" && len(f.Metrics) > 0 {
			if f.Metrics[0].Value != 5.5 {
				t.Errorf("expected 5.5, got %f", f.Metrics[0].Value)
			}
		}
	}
}

func TestGaugeWithStatic_Sub(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	gauge := Gauge(ctx, "test_gauge_sub", "test")
	gauge.Set(10)
	gauge.Sub(3)

	b := FromContext(ctx)
	families := b.Metrics().Gather()
	for _, f := range families {
		if f.Name == "test_gauge_sub" && len(f.Metrics) > 0 {
			if f.Metrics[0].Value != 7 {
				t.Errorf("expected 7, got %f", f.Metrics[0].Value)
			}
		}
	}
}

// ── Op.Event (api.go) ──────────────────────────────────────────────────────

func TestOp_Event(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	op, ctx := Operation(ctx, "test_event_op")
	op.Event(ctx, attr.NewEvent("cache.hit", attr.String("key", "user:1")))
	op.Done()
}

func TestOp_Event_NilState(t *testing.T) {
	// Noop op with nil state should not panic
	op := &Op{}
	op.Event(context.Background(), attr.NewEvent("test"))
}

func TestOp_Event_NoSpan(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	// Use NoTrace so span is nil
	op, ctx := Operation(ctx, "no_trace_op", NoTrace())
	op.Event(ctx, attr.NewEvent("test"))
	op.Done()
}

// ── Src.Sum, Src.Gauge, Src.Histogram (api.go) ─────────────────────────────

func TestSrc_Sum(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	source, ctx := Source(ctx, "worker")
	source.Sum(ctx, "jobs", 5)
	source.Done()

	b := FromContext(ctx)
	families := b.Metrics().Gather()
	found := false
	for _, f := range families {
		if f.Name == "worker_jobs" {
			found = true
		}
	}
	if !found {
		t.Error("expected worker_jobs counter")
	}
}

func TestSrc_Gauge(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	source, ctx := Source(ctx, "worker")
	source.Gauge(ctx, "queue_depth", 42)
	source.Done()

	b := FromContext(ctx)
	families := b.Metrics().Gather()
	found := false
	for _, f := range families {
		if f.Name == "worker_queue_depth" {
			found = true
		}
	}
	if !found {
		t.Error("expected worker_queue_depth gauge")
	}
}

func TestSrc_Histogram(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	source, ctx := Source(ctx, "worker")
	source.Histogram(ctx, "latency_ms", 52.3)
	source.Done()

	b := FromContext(ctx)
	families := b.Metrics().Gather()
	found := false
	for _, f := range families {
		if f.Name == "worker_latency_ms" {
			found = true
		}
	}
	if !found {
		t.Error("expected worker_latency_ms histogram")
	}
}

func TestSrc_Noop(t *testing.T) {
	ctx := context.Background()
	source, ctx := Source(ctx, "worker")
	// Should not panic on noop
	source.Sum(ctx, "jobs", 1)
	source.Gauge(ctx, "depth", 5)
	source.Histogram(ctx, "lat", 10)
	source.Done()
}

// ── MetricsRegistry (bedrock.go) ────────────────────────────────────────────

func TestMetricsRegistry(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	b := FromContext(ctx)
	registry := b.MetricsRegistry()
	if registry == nil {
		t.Fatal("MetricsRegistry returned nil")
	}
}

// ── instrumentedTransport.RoundTrip (client.go) ────────────────────────────

func TestInstrumentedTransport_RoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	op, ctx := Operation(ctx, "test_op")
	defer op.Done()

	client := NewClient(nil)
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestInstrumentedTransport_RoundTrip_NoBedrock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewClient(nil)
	req, _ := http.NewRequestWithContext(context.Background(), "GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	_ = resp.Body.Close()
}

// ── Get with invalid URL (client.go: Get 75% coverage) ─────────────────────

func TestGet_InvalidURL(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	_, err := Get(ctx, "://invalid-url")
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

// ── MustFromEnv (config.go) ─────────────────────────────────────────────────

func TestMustFromEnv(t *testing.T) {
	// Should not panic with valid env (defaults)
	cfg := MustFromEnv()
	if cfg.Service == "" {
		t.Error("expected non-empty service name")
	}
}

// ── useGRPC (config.go) ─────────────────────────────────────────────────────

func TestUseGRPC(t *testing.T) {
	tests := []struct {
		name     string
		cfg      Config
		expected bool
	}{
		{"explicit http", Config{TraceProtocol: "http"}, false},
		{"explicit http/json", Config{TraceProtocol: "http/json"}, false},
		{"explicit http/protobuf", Config{TraceProtocol: "http/protobuf"}, false},
		{"explicit grpc", Config{TraceProtocol: "grpc"}, true},
		{"explicit grpc/proto", Config{TraceProtocol: "grpc/proto"}, true},
		{"auto-detect http URL", Config{TraceURL: "http://localhost:4318/v1/traces"}, false},
		{"auto-detect https URL", Config{TraceURL: "https://collector.example.com/v1/traces"}, false},
		{"auto-detect bare host:port", Config{TraceURL: "localhost:4317"}, true},
		{"auto-detect empty", Config{TraceURL: ""}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := useGRPC(tt.cfg)
			if got != tt.expected {
				t.Errorf("useGRPC(%+v) = %v, want %v", tt.cfg, got, tt.expected)
			}
		})
	}
}

// ── withNoTrace (context.go) ────────────────────────────────────────────────

func TestWithNoTrace(t *testing.T) {
	ctx := context.Background()
	if isNoTrace(ctx) {
		t.Error("expected no-trace to be false initially")
	}

	ctx = withNoTrace(ctx)
	if !isNoTrace(ctx) {
		t.Error("expected no-trace to be true after withNoTrace")
	}
}

// ── NoTrace option (options.go) ─────────────────────────────────────────────

func TestNoTrace(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	op, opCtx := Operation(ctx, "no_trace_op", NoTrace())
	defer op.Done()

	// Span should be nil for NoTrace operations
	span := trace.SpanFromContext(opCtx)
	if span != nil {
		t.Error("expected nil span with NoTrace")
	}
}

func TestNoTrace_InheritedByChildren(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	op, opCtx := Operation(ctx, "parent", NoTrace())
	defer op.Done()

	// Child should also be no-trace
	childOp, childCtx := Operation(opCtx, "child")
	defer childOp.Done()

	span := trace.SpanFromContext(childCtx)
	if span != nil {
		t.Error("expected nil span for child of NoTrace parent")
	}
}

// ── Success and Failure options (options.go) ────────────────────────────────

func TestSuccess(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	op, _ := Operation(ctx, "success_op", Success())
	op.Done()
	// The operation should complete without error
}

func TestFailure(t *testing.T) {
	ctx, cleanup := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer cleanup()

	op, _ := Operation(ctx, "failure_op", Failure(errors.New("test error")))
	op.Done()
}

func TestEndSuccess(t *testing.T) {
	// EndSuccess returns a function that configures endConfig
	opt := EndSuccess()
	cfg := &endConfig{}
	opt(cfg)
	if !cfg.success {
		t.Error("EndSuccess should set success=true")
	}
	if !cfg.hasOpts {
		t.Error("EndSuccess should set hasOpts=true")
	}
}

func TestEndFailure(t *testing.T) {
	// EndFailure returns a function that configures endConfig
	testErr := errors.New("test error")
	opt := EndFailure(testErr)
	cfg := &endConfig{}
	opt(cfg)
	if cfg.success {
		t.Error("EndFailure should set success=false")
	}
	if cfg.failure != testErr {
		t.Errorf("EndFailure should set failure to the error, got %v", cfg.failure)
	}
	if !cfg.hasOpts {
		t.Error("EndFailure should set hasOpts=true")
	}
}

// ── Middleware options (middleware.go) ───────────────────────────────────────

func TestWithAdditionalLabels(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := HTTPMiddleware(ctx, handler,
		WithAdditionalLabels("user_agent", "content_type"),
	)

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestWithSuccessCodes(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	var opState *operationState
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		opState = operationStateFromContext(r.Context())
		w.WriteHeader(http.StatusNotFound)
	})

	// Treat 404 as success
	wrapped := HTTPMiddleware(ctx, handler,
		WithSuccessCodes(200, 404),
	)

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if !opState.success {
		t.Error("expected operation to be marked as success for 404 (custom success code)")
	}
}

func TestWithSuccessCodes_NonSuccess(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	var opState *operationState
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		opState = operationStateFromContext(r.Context())
		w.WriteHeader(http.StatusInternalServerError)
	})

	// Only 200 is success
	wrapped := HTTPMiddleware(ctx, handler,
		WithSuccessCodes(200),
	)

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if opState.success {
		t.Error("expected operation to be marked as failure for 500 (not in success codes)")
	}
}

func TestWithTracePropagation_Disabled(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := HTTPMiddleware(ctx, handler,
		WithTracePropagation(false),
	)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

// ── responseWriter.Write (middleware.go) ────────────────────────────────────

func TestResponseWriter_Write(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Call Write without calling WriteHeader first — should auto-set 200
		_, _ = w.Write([]byte("hello"))
	})

	wrapped := HTTPMiddleware(ctx, handler)
	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Body.String() != "hello" {
		t.Errorf("expected body 'hello', got %q", rr.Body.String())
	}
	if rr.Code != 200 {
		t.Errorf("expected auto status 200, got %d", rr.Code)
	}
}

// ── logCanonical (operation.go) ─────────────────────────────────────────────

func TestLogCanonical(t *testing.T) {
	var buf bytes.Buffer
	ctx, close := Init(context.Background(),
		WithConfig(Config{
			Service:      "test",
			LogCanonical: true,
			LogOutput:    &buf,
			LogFormat:    "json",
		}),
	)
	defer close()

	op, ctx := Operation(ctx, "canonical_op",
		Attrs(attr.String("user_id", "123")),
		MetricLabels("user_id"),
	)
	op.Register(ctx, attr.String("status", "active"))
	op.Done()

	output := buf.String()
	if output == "" {
		t.Error("expected canonical log output")
	}
}

func TestLogCanonical_WithError(t *testing.T) {
	var buf bytes.Buffer
	ctx, close := Init(context.Background(),
		WithConfig(Config{
			Service:      "test",
			LogCanonical: true,
			LogOutput:    &buf,
			LogFormat:    "json",
		}),
	)
	defer close()

	op, ctx := Operation(ctx, "canonical_error_op")
	op.Register(ctx, attr.Error(errors.New("test error")))
	op.Done()

	output := buf.String()
	if output == "" {
		t.Error("expected canonical log output for error")
	}
}

// ── Operation.Register (operation.go) ───────────────────────────────────────

func TestOp_Register_Multiple(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	op, ctx := Operation(ctx, "register_op",
		MetricLabels("status"),
	)
	op.Register(ctx, attr.String("status", "ok"))
	op.Register(ctx, attr.Int("count", 42))
	op.Register(ctx) // Empty — should be noop
	op.Done()
}

func TestOp_Register_Nil(t *testing.T) {
	op := &Op{}
	// Should not panic
	op.Register(context.Background(), attr.String("key", "value"))
}

// ── Bedrock.Shutdown partial coverage (bedrock.go) ─────────────────────────

func TestBedrock_Shutdown(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{
			Service:  "test",
			TraceURL: "http://localhost:99999/v1/traces",
		}),
	)
	defer close()

	b := FromContext(ctx)
	err := b.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("Shutdown error: %v", err)
	}
}

// ── FromEnv (config.go) partial coverage ────────────────────────────────────

func TestFromEnv_Defaults(t *testing.T) {
	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv error: %v", err)
	}
	if cfg.Service != "unknown" {
		t.Errorf("expected default service 'unknown', got %q", cfg.Service)
	}
	if cfg.TraceSampleRate != 1.0 {
		t.Errorf("expected sample rate 1.0, got %f", cfg.TraceSampleRate)
	}
}

// ── Init with server disabled ───────────────────────────────────────────────

func TestInit_ServerDisabled(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{
			Service:       "test",
			ServerEnabled: false,
		}),
	)
	defer close()

	b := FromContext(ctx)
	if b == nil || b.isNoop {
		t.Error("expected initialized bedrock")
	}
}

// ── Source with SourceAttrs and SourceMetricLabels ──────────────────────────

func TestSource_ChildOperationInheritsPrefix(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	source, ctx := Source(ctx, "bg.worker",
		SourceAttrs(attr.String("worker.type", "async")),
		SourceMetricLabels("worker.type"),
	)

	op, _ := Operation(ctx, "process")
	// Operation name should be prefixed
	if op.state.name != "bg.worker.process" {
		t.Errorf("expected 'bg.worker.process', got %q", op.state.name)
	}
	op.Done()
	source.Done()
}

// ── Source.Done with canonical logging ──────────────────────────────────────

func TestSource_Done_Canonical(t *testing.T) {
	var buf bytes.Buffer
	ctx, close := Init(context.Background(),
		WithConfig(Config{
			Service:      "test",
			LogCanonical: true,
			LogOutput:    &buf,
			LogFormat:    "json",
		}),
	)
	defer close()

	source, _ := Source(ctx, "bg.worker")
	source.Done()

	if buf.Len() == 0 {
		t.Error("expected canonical log output for source done")
	}
}

// ── buildMetricLabels partial coverage (operation.go) ───────────────────────

func TestBuildMetricLabels_MissingLabel(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer close()

	op, _ := Operation(ctx, "missing_label_op",
		MetricLabels("method", "status"),
	)
	// Only register method, not status — status should default to "_"
	op.Register(context.Background(), attr.String("method", "GET"))
	op.Done()
}

// ── Op.Done when state is nil (api.go) ──────────────────────────────────────

func TestOp_Done_Nil(t *testing.T) {
	op := &Op{}
	// Should not panic
	op.Done()
}

// ── Concurrent metrics updates ──────────────────────────────────────────────

func TestConcurrentMetrics(t *testing.T) {
	ctx, cleanup := Init(context.Background(),
		WithConfig(Config{Service: "test"}),
	)
	defer cleanup()

	counter := Counter(ctx, "concurrent_counter", "test", "label")
	gauge := Gauge(ctx, "concurrent_gauge", "test")

	ch := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			counter.With(attr.String("label", "a")).Inc()
			gauge.Inc()
			<-ch
		}()
	}
	close(ch)
	time.Sleep(50 * time.Millisecond)
}

// ── isHexString (env_detect.go) ─────────────────────────────────────────────

func TestIsHexString(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"0123456789abcdef", true},
		{"ABCDEF", true},
		{"abc123", true},
		{"xyz", false},
		{"abc_123", false},
		{"", true}, // empty is technically valid
	}
	for _, tt := range tests {
		got := isHexString(tt.s)
		if got != tt.want {
			t.Errorf("isHexString(%q) = %v, want %v", tt.s, got, tt.want)
		}
	}
}

// ── extractContainerID (env_detect.go) ──────────────────────────────────────

func TestExtractContainerID(t *testing.T) {
	validID := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	tests := []struct {
		name string
		line string
		want string
	}{
		{"docker cgroup", "12:cpuset:/docker/" + validID, validID},
		{"docker scope", "1:name=systemd:/docker-" + validID + ".scope", validID},
		{"containerd scope", "1:name=systemd:/containerd-" + validID + ".scope", validID},
		{"no container", "1:name=systemd:/user.slice", ""},
		{"short hex", "1:name=systemd:/docker/abc123", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractContainerID(tt.line)
			if got != tt.want {
				t.Errorf("extractContainerID(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}

// ── detectAWSWithBase / detectGCPWithBase ───────────────────────────────────

func TestDetectAWSWithBase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest/meta-data/instance-id":
			_, _ = w.Write([]byte("i-1234567890abcdef0"))
		case "/latest/meta-data/placement/region":
			_, _ = w.Write([]byte("us-east-1"))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	attrs := detectAWSWithBase(srv.URL)
	if len(attrs) == 0 {
		t.Fatal("expected AWS attributes")
	}

	foundProvider := false
	foundInstanceID := false
	foundRegion := false
	for _, a := range attrs {
		switch a.Key {
		case "cloud.provider":
			foundProvider = a.Value.AsString() == "aws"
		case "host.id":
			foundInstanceID = a.Value.AsString() == "i-1234567890abcdef0"
		case "cloud.region":
			foundRegion = a.Value.AsString() == "us-east-1"
		}
	}
	if !foundProvider {
		t.Error("expected cloud.provider=aws")
	}
	if !foundInstanceID {
		t.Error("expected host.id")
	}
	if !foundRegion {
		t.Error("expected cloud.region=us-east-1")
	}
}

func TestDetectGCPWithBase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			w.WriteHeader(403)
			return
		}
		switch r.URL.Path {
		case "/computeMetadata/v1/instance/id":
			_, _ = w.Write([]byte("1234567890"))
		case "/computeMetadata/v1/instance/zone":
			_, _ = w.Write([]byte("projects/123/zones/us-central1-a"))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	attrs := detectGCPWithBase(srv.URL)
	if len(attrs) == 0 {
		t.Fatal("expected GCP attributes")
	}

	foundProvider := false
	foundRegion := false
	for _, a := range attrs {
		switch a.Key {
		case "cloud.provider":
			foundProvider = a.Value.AsString() == "gcp"
		case "cloud.region":
			foundRegion = a.Value.AsString() == "us-central1"
		}
	}
	if !foundProvider {
		t.Error("expected cloud.provider=gcp")
	}
	if !foundRegion {
		t.Error("expected cloud.region=us-central1")
	}
}

func TestDetectAWSWithBase_NoServer(t *testing.T) {
	attrs := detectAWSWithBase("http://127.0.0.1:1")
	if len(attrs) != 0 {
		t.Error("expected no attributes when server unreachable")
	}
}

func TestDetectGCPWithBase_NoServer(t *testing.T) {
	attrs := detectGCPWithBase("http://127.0.0.1:1")
	if len(attrs) != 0 {
		t.Error("expected no attributes when server unreachable")
	}
}
