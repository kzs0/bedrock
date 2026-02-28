package bedrock_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kzs0/bedrock"
	"github.com/kzs0/bedrock/attr"
)

// initBench creates a minimal Bedrock context with the obs server disabled.
func initBench(b *testing.B) (context.Context, func()) {
	b.Helper()
	ctx, close := bedrock.Init(context.Background(), bedrock.WithConfig(bedrock.Config{
		Service:       "bench",
		ServerEnabled: false,
	}))
	return ctx, close
}

// ── NOOP (uninitialized context) ──────────────────────────────────────────────

func BenchmarkOperation_Noop(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		op, ctx2 := bedrock.Operation(ctx, "op")
		op.Done()
		_ = ctx2
	}
}

func BenchmarkStep_Noop(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		step := bedrock.Step(ctx, "step")
		step.Done()
	}
}

func BenchmarkInfo_Noop(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bedrock.Info(ctx, "hello", attr.String("key", "value"))
	}
}

// ── OPERATION lifecycle ────────────────────────────────────────────────────────

// NoTrace: span disabled, only metrics + context alloc.
func BenchmarkOperation_NoTrace(b *testing.B) {
	ctx, close := initBench(b)
	defer close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		op, ctx2 := bedrock.Operation(ctx, "op", bedrock.NoTrace())
		op.Done()
		_ = ctx2
	}
}

// Basic: full span (TraceID/SpanID via crypto/rand) + metrics, no export target.
func BenchmarkOperation_Basic(b *testing.B) {
	ctx, close := initBench(b)
	defer close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		op, ctx2 := bedrock.Operation(ctx, "op")
		op.Done()
		_ = ctx2
	}
}

// WithAttrs: attrs declared upfront + one dynamic Register + metric label.
func BenchmarkOperation_WithAttrs(b *testing.B) {
	ctx, close := initBench(b)
	defer close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		op, ctx2 := bedrock.Operation(ctx, "op",
			bedrock.Attrs(attr.String("user_id", "u123"), attr.String("region", "us-east-1")),
			bedrock.MetricLabels("region"),
		)
		op.Register(ctx2, attr.String("status", "ok"))
		op.Done()
	}
}

// WithError: failure path — attr.Error triggers span.RecordError.
func BenchmarkOperation_WithError(b *testing.B) {
	ctx, close := initBench(b)
	defer close()
	someErr := attr.Error(context.Canceled)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		op, ctx2 := bedrock.Operation(ctx, "op")
		op.Register(ctx2, someErr)
		op.Done()
	}
}

// Nested: child op shares parent span as parent_span_id.
func BenchmarkOperation_Nested(b *testing.B) {
	ctx, close := initBench(b)
	defer close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parent, pCtx := bedrock.Operation(ctx, "parent")
		child, _ := bedrock.Operation(pCtx, "child")
		child.Done()
		parent.Done()
	}
}

// ── STEP lifecycle ─────────────────────────────────────────────────────────────

// Step within an active operation (has parent span reference).
func BenchmarkStep_Basic(b *testing.B) {
	ctx, close := initBench(b)
	defer close()
	op, opCtx := bedrock.Operation(ctx, "parent")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		step := bedrock.Step(opCtx, "child")
		step.Done()
	}
	op.Done()
}

// Step with attribute registration.
func BenchmarkStep_WithAttrs(b *testing.B) {
	ctx, close := initBench(b)
	defer close()
	op, opCtx := bedrock.Operation(ctx, "parent", bedrock.MetricLabels("rows"))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		step := bedrock.Step(opCtx, "query")
		step.Register(opCtx, attr.Int("rows", 42))
		step.Done()
	}
	op.Done()
}

// ── SOURCE ─────────────────────────────────────────────────────────────────────

func BenchmarkSource_Basic(b *testing.B) {
	ctx, close := initBench(b)
	defer close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		src, sCtx := bedrock.Source(ctx, "worker",
			bedrock.SourceAttrs(attr.String("worker.type", "async")),
			bedrock.SourceMetricLabels("worker.type"),
		)
		// One child operation inheriting source prefix
		op, _ := bedrock.Operation(sCtx, "process")
		op.Done()
		src.Done()
	}
}

func BenchmarkSource_Sum(b *testing.B) {
	ctx, close := initBench(b)
	defer close()
	src, sCtx := bedrock.Source(ctx, "worker")
	defer src.Done()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		src.Sum(sCtx, "jobs", 1)
	}
}

// ── LOGGING ───────────────────────────────────────────────────────────────────

func BenchmarkInfo_Basic(b *testing.B) {
	ctx, close := initBench(b)
	defer close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bedrock.Info(ctx, "event", attr.String("key", "value"), attr.Int("count", 1))
	}
}

// ── METRICS ───────────────────────────────────────────────────────────────────

// Counter.Inc with pre-resolved *CounterVec (hot path: zero alloc expected).
func BenchmarkCounter_Inc(b *testing.B) {
	ctx, close := initBench(b)
	defer close()
	c := bedrock.Counter(ctx, "bench_req", "Total requests", "method", "status")
	cv := c.With(attr.String("method", "GET"), attr.String("status", "200"))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cv.Inc()
	}
}

// Counter.With resolves label set each call (map lookup + slice alloc).
func BenchmarkCounter_With(b *testing.B) {
	ctx, close := initBench(b)
	defer close()
	c := bedrock.Counter(ctx, "bench_req2", "Total requests", "method", "status")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.With(attr.String("method", "GET"), attr.String("status", "200")).Inc()
	}
}

// Gauge with pre-resolved vec.
func BenchmarkGauge_Set(b *testing.B) {
	ctx, close := initBench(b)
	defer close()
	g := bedrock.Gauge(ctx, "bench_active", "Active conns", "pool")
	gv := g.With(attr.String("pool", "primary"))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gv.Set(float64(i % 1000))
	}
}

// Histogram.Observe with pre-resolved vec (binary search over bucket bounds).
func BenchmarkHistogram_Observe(b *testing.B) {
	ctx, close := initBench(b)
	defer close()
	h := bedrock.Histogram(ctx, "bench_dur_ms", "Duration", nil, "endpoint")
	hv := h.With(attr.String("endpoint", "/users"))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hv.Observe(float64(i % 1000))
	}
}

// ── HTTP MIDDLEWARE ────────────────────────────────────────────────────────────

// Middleware_Basic: full request round-trip through HTTPMiddleware.
func BenchmarkMiddleware_Basic(b *testing.B) {
	ctx, close := initBench(b)
	defer close()

	handler := bedrock.HTTPMiddleware(ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/users", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}

// Middleware_WithPropagation: includes W3C traceparent header extraction.
func BenchmarkMiddleware_WithPropagation(b *testing.B) {
	ctx, close := initBench(b)
	defer close()

	handler := bedrock.HTTPMiddleware(ctx,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		bedrock.WithTracePropagation(true),
	)

	req := httptest.NewRequest("GET", "/api/users", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}
