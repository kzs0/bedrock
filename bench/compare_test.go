// Package compare_test benchmarks Bedrock against OTel SDK + Prometheus
// performing equivalent work, so the numbers are directly comparable.
//
// Run with:
//
//	cd bench && go test -bench=. -benchmem -benchtime=3s
package compare_test

import (
	"context"
	"testing"
	"time"

	"github.com/kzs0/bedrock"
	"github.com/kzs0/bedrock/attr"

	"github.com/prometheus/client_golang/prometheus"

	otelattr "go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// ── Setup helpers ──────────────────────────────────────────────────────────────

func initBedrock(b *testing.B) (context.Context, func()) {
	b.Helper()
	ctx, close := bedrock.Init(context.Background(), bedrock.WithConfig(bedrock.Config{
		Service:       "bench",
		ServerEnabled: false,
	}))
	return ctx, close
}

// initOTelTracer creates a real SDK tracer with no processor.
// Spans get real trace/span IDs but are not batched or exported — matching the
// Bedrock benchmark setup (no TraceURL configured).
func initOTelTracer(b *testing.B) oteltrace.Tracer {
	b.Helper()
	tp := sdktrace.NewTracerProvider()
	return tp.Tracer("bench")
}

// initOTelMeter creates a real SDK meter backed by a manual reader (no periodic export).
func initOTelMeter(b *testing.B) metric.Meter {
	b.Helper()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader()))
	return mp.Meter("bench")
}

// ── SPANS: Bedrock vs OTel SDK ────────────────────────────────────────────────
// Bedrock Operation = span + 4 auto-metrics (count/successes/failures/duration).
// OTel span = span only. The rows show what each system does for the same call.

func BenchmarkBedrock_Span_Basic(b *testing.B) {
	ctx, close := initBedrock(b)
	defer close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		op, ctx2 := bedrock.Operation(ctx, "op")
		op.Done()
		_ = ctx2
	}
}

func BenchmarkOTel_Span_Basic(b *testing.B) {
	tracer := initOTelTracer(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx2, span := tracer.Start(ctx, "op")
		span.End()
		_ = ctx2
	}
}

func BenchmarkBedrock_Span_WithAttrs(b *testing.B) {
	ctx, close := initBedrock(b)
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

func BenchmarkOTel_Span_WithAttrs(b *testing.B) {
	tracer := initOTelTracer(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx2, span := tracer.Start(ctx, "op",
			oteltrace.WithAttributes(
				otelattr.String("user_id", "u123"),
				otelattr.String("region", "us-east-1"),
			),
		)
		span.SetAttributes(otelattr.String("status", "ok"))
		span.End()
		_ = ctx2
	}
}

// ── COUNTERS ──────────────────────────────────────────────────────────────────
// Hot path: pre-resolved label set, just the atomic increment.

func BenchmarkBedrock_Counter_Inc(b *testing.B) {
	ctx, close := initBedrock(b)
	defer close()
	c := bedrock.Counter(ctx, "bench_req", "Total requests", "method", "status")
	cv := c.With(attr.String("method", "GET"), attr.String("status", "200"))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cv.Inc()
	}
}

func BenchmarkProm_Counter_Inc(b *testing.B) {
	reg := prometheus.NewRegistry()
	c := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "bench_req", Help: "Total requests",
	}, []string{"method", "status"})
	reg.MustRegister(c)
	cv := c.With(prometheus.Labels{"method": "GET", "status": "200"})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cv.Inc()
	}
}

// Per-call label resolution: label lookup + atomic increment each iteration.

func BenchmarkBedrock_Counter_With(b *testing.B) {
	ctx, close := initBedrock(b)
	defer close()
	c := bedrock.Counter(ctx, "bench_req2", "Total requests", "method", "status")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.With(attr.String("method", "GET"), attr.String("status", "200")).Inc()
	}
}

func BenchmarkProm_Counter_With(b *testing.B) {
	reg := prometheus.NewRegistry()
	c := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "bench_req3", Help: "Total requests",
	}, []string{"method", "status"})
	reg.MustRegister(c)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.With(prometheus.Labels{"method": "GET", "status": "200"}).Inc()
	}
}

// OTel metrics have no pre-resolution API; attributes are always per-call.

func BenchmarkOTel_Counter_Add(b *testing.B) {
	meter := initOTelMeter(b)
	ctx := context.Background()
	counter, _ := meter.Int64Counter("bench_req4")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		counter.Add(ctx, 1,
			metric.WithAttributes(
				otelattr.String("method", "GET"),
				otelattr.String("status", "200"),
			),
		)
	}
}

// ── HISTOGRAMS ────────────────────────────────────────────────────────────────
// Hot path: pre-resolved label set, binary-search bucket + atomic update.

func BenchmarkBedrock_Histogram_Observe(b *testing.B) {
	ctx, close := initBedrock(b)
	defer close()
	h := bedrock.Histogram(ctx, "bench_dur_ms", "Duration", nil, "endpoint")
	hv := h.With(attr.String("endpoint", "/users"))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hv.Observe(float64(i % 1000))
	}
}

func BenchmarkProm_Histogram_Observe(b *testing.B) {
	reg := prometheus.NewRegistry()
	h := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "bench_dur_ms", Help: "Duration",
	}, []string{"endpoint"})
	reg.MustRegister(h)
	hv := h.With(prometheus.Labels{"endpoint": "/users"})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hv.Observe(float64(i % 1000))
	}
}

func BenchmarkOTel_Histogram_Record(b *testing.B) {
	meter := initOTelMeter(b)
	ctx := context.Background()
	hist, _ := meter.Float64Histogram("bench_dur_ms2")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hist.Record(ctx, float64(i%1000),
			metric.WithAttributes(otelattr.String("endpoint", "/users")),
		)
	}
}

// ── COMBINED OPERATION ────────────────────────────────────────────────────────
// Apples-to-apples: both do a span + count/success/duration metrics.
// Bedrock wraps this in a single Operation(); OTel+Prom requires manual calls.

// Bedrock Operation: span + 4 auto-metrics with no attrs (matches Basic above).
func BenchmarkBedrock_Operation_Basic(b *testing.B) {
	ctx, close := initBedrock(b)
	defer close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		op, ctx2 := bedrock.Operation(ctx, "op")
		op.Done()
		_ = ctx2
	}
}

// Manual OTel+Prom equivalent of Bedrock's Operation_Basic.
// User must write this themselves; Bedrock generates it automatically.
func BenchmarkOTelProm_Operation_Basic(b *testing.B) {
	tracer := initOTelTracer(b)
	reg := prometheus.NewRegistry()
	opCount := prometheus.NewCounter(prometheus.CounterOpts{Name: "op_count", Help: "Total"})
	opSuccess := prometheus.NewCounter(prometheus.CounterOpts{Name: "op_successes", Help: "Successes"})
	opDuration := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "op_duration_ms", Help: "Duration ms"})
	reg.MustRegister(opCount, opSuccess, opDuration)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		ctx2, span := tracer.Start(ctx, "op")
		span.End()
		opCount.Inc()
		opSuccess.Inc()
		opDuration.Observe(float64(time.Since(start).Milliseconds()))
		_ = ctx2
	}
}

// Bedrock Operation with attrs + metric label — span + 4 auto-metrics w/ label resolution.
func BenchmarkBedrock_Operation_WithAttrs(b *testing.B) {
	ctx, close := initBedrock(b)
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

// Manual OTel+Prom equivalent of Operation_WithAttrs (with one label dimension).
func BenchmarkOTelProm_Operation_WithAttrs(b *testing.B) {
	tracer := initOTelTracer(b)
	reg := prometheus.NewRegistry()
	opCount := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "op_count", Help: "Total"}, []string{"region"})
	opSuccess := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "op_successes", Help: "Successes"}, []string{"region"})
	opDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "op_duration_ms", Help: "Duration ms"}, []string{"region"})
	reg.MustRegister(opCount, opSuccess, opDuration)
	ctx := context.Background()
	regionLabel := prometheus.Labels{"region": "us-east-1"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		ctx2, span := tracer.Start(ctx, "op",
			oteltrace.WithAttributes(
				otelattr.String("user_id", "u123"),
				otelattr.String("region", "us-east-1"),
			),
		)
		span.SetAttributes(otelattr.String("status", "ok"))
		span.End()
		opCount.With(regionLabel).Inc()
		opSuccess.With(regionLabel).Inc()
		opDuration.With(regionLabel).Observe(float64(time.Since(start).Milliseconds()))
		_ = ctx2
	}
}
