package trace

import (
	"context"
	"testing"

	"github.com/kzs0/bedrock/internal"
)

func TestNewRemoteSpanContext(t *testing.T) {
	traceID := internal.NewTraceID()
	spanID := internal.NewSpanID()

	sc := NewRemoteSpanContext(traceID, spanID, "vendor1=value1", true)

	if sc.TraceID != traceID {
		t.Error("TraceID mismatch")
	}
	if sc.SpanID != spanID {
		t.Error("SpanID mismatch")
	}
	if sc.Tracestate != "vendor1=value1" {
		t.Errorf("Tracestate: got %q, want 'vendor1=value1'", sc.Tracestate)
	}
	if !sc.IsRemote {
		t.Error("expected IsRemote=true")
	}
	if !sc.Sampled {
		t.Error("expected Sampled=true")
	}
	if !sc.IsValid() {
		t.Error("expected valid span context")
	}
}

func TestSpanContextFromContext_NoSpan(t *testing.T) {
	ctx := context.Background()
	sc := SpanContextFromContext(ctx)

	if sc.IsValid() {
		t.Error("expected invalid span context from empty context")
	}
	if sc.IsRemote {
		t.Error("expected IsRemote=false")
	}
}

func TestSpanContextFromContext_WithSpan(t *testing.T) {
	tracer := NewTracer(TracerConfig{ServiceName: "test"})
	ctx, span := tracer.Start(context.Background(), "test-op")
	defer span.End()

	sc := SpanContextFromContext(ctx)

	if !sc.IsValid() {
		t.Error("expected valid span context")
	}
	if sc.TraceID != span.TraceID() {
		t.Error("TraceID mismatch")
	}
	if sc.SpanID != span.SpanID() {
		t.Error("SpanID mismatch")
	}
	if sc.IsRemote {
		t.Error("local span context should have IsRemote=false")
	}
	if !sc.Sampled {
		t.Error("active span should be sampled")
	}
}

func TestContextWithSpan_SpanFromContext(t *testing.T) {
	tracer := NewTracer(TracerConfig{ServiceName: "test"})
	_, span := tracer.Start(context.Background(), "test")
	defer span.End()

	ctx := ContextWithSpan(context.Background(), span)
	retrieved := SpanFromContext(ctx)

	if retrieved != span {
		t.Error("SpanFromContext should return the span set by ContextWithSpan")
	}
}

func TestSpanFromContext_NoSpan(t *testing.T) {
	span := SpanFromContext(context.Background())
	if span != nil {
		t.Error("expected nil span from empty context")
	}
}
