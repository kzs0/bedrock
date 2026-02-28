package bedrock_test

import (
	"context"
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

// BenchmarkOperation_Noop measures the overhead when bedrock is NOT in context.
// This represents library code that calls Operation without initialization.
func BenchmarkOperation_Noop(b *testing.B) {
	ctx := context.Background() // no bedrock
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		op, ctx2 := bedrock.Operation(ctx, "noop_op")
		op.Done()
		_ = ctx2
	}
}

// BenchmarkOperation_NoTrace measures an operation with tracing disabled.
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

// BenchmarkOperation_Basic measures a full operation with a span (no export target).
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

// BenchmarkOperation_WithAttrs measures an operation that registers attributes.
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

// BenchmarkOperation_WithError measures the failure path (registers an error).
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

// BenchmarkStep_Noop measures Step when bedrock is not in context.
func BenchmarkStep_Noop(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		step := bedrock.Step(ctx, "step")
		step.Done()
	}
}

// BenchmarkStep_Basic measures a Step within an active operation.
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

// BenchmarkCounter_Inc measures a counter increment with pre-resolved label values.
func BenchmarkCounter_Inc(b *testing.B) {
	ctx, close := initBench(b)
	defer close()
	c := bedrock.Counter(ctx, "bench_requests", "Total requests", "method", "status")
	cv := c.With(attr.String("method", "GET"), attr.String("status", "200"))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cv.Inc()
	}
}

// BenchmarkCounter_With measures the With() label resolution path.
func BenchmarkCounter_With(b *testing.B) {
	ctx, close := initBench(b)
	defer close()
	c := bedrock.Counter(ctx, "bench_requests2", "Total requests", "method", "status")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.With(attr.String("method", "GET"), attr.String("status", "200")).Inc()
	}
}

// BenchmarkHistogram_Observe measures a histogram observation.
func BenchmarkHistogram_Observe(b *testing.B) {
	ctx, close := initBench(b)
	defer close()
	h := bedrock.Histogram(ctx, "bench_duration_ms", "Duration", nil, "endpoint")
	hv := h.With(attr.String("endpoint", "/users"))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hv.Observe(float64(i % 1000))
	}
}

// BenchmarkInfo_Noop measures logging when bedrock is not initialized.
func BenchmarkInfo_Noop(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bedrock.Info(ctx, "hello", attr.String("key", "value"))
	}
}
