package trace

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
)

func TestTracerStartSpan(t *testing.T) {
	tracer := NewTracer(TracerConfig{
		ServiceName: "test-service",
	})

	ctx, span := tracer.Start(context.Background(), "test.operation")
	defer span.End()

	if span.Name() != "test.operation" {
		t.Errorf("expected name 'test.operation', got %q", span.Name())
	}
	if span.TraceID().IsZero() {
		t.Error("expected non-zero trace ID")
	}
	if span.SpanID().IsZero() {
		t.Error("expected non-zero span ID")
	}
	if !span.ParentID().IsZero() {
		t.Error("expected zero parent ID for root span")
	}

	// Verify span is in context
	spanFromCtx := SpanFromContext(ctx)
	if spanFromCtx != span {
		t.Error("expected span from context to match")
	}
}

func TestNestedSpans(t *testing.T) {
	tracer := NewTracer(TracerConfig{
		ServiceName: "test-service",
	})

	ctx, parent := tracer.Start(context.Background(), "parent")
	defer parent.End()

	_, child := tracer.Start(ctx, "child")
	defer child.End()

	if child.TraceID() != parent.TraceID() {
		t.Error("child should have same trace ID as parent")
	}
	if child.ParentID() != parent.SpanID() {
		t.Error("child's parent ID should be parent's span ID")
	}
}

func TestSpanAttributes(t *testing.T) {
	tracer := NewTracer(TracerConfig{})

	_, span := tracer.Start(context.Background(), "test",
		WithAttrs(attr.String("initial", "value")),
	)
	defer span.End()

	span.SetAttr(attr.Int("count", 42))

	attrs := span.Attrs()
	if attrs.Len() != 2 {
		t.Errorf("expected 2 attrs, got %d", attrs.Len())
	}

	v, ok := attrs.Get("initial")
	if !ok || v.AsString() != "value" {
		t.Error("expected 'initial' attr")
	}

	v, ok = attrs.Get("count")
	if !ok || v.AsInt64() != 42 {
		t.Error("expected 'count' attr")
	}
}

func TestSpanEvents(t *testing.T) {
	tracer := NewTracer(TracerConfig{})

	_, span := tracer.Start(context.Background(), "test")

	span.AddEvent("event1", attr.String("key", "value"))
	span.AddEvent("event2")

	events := span.Events()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	if events[0].Name != "event1" {
		t.Errorf("expected event name 'event1', got %q", events[0].Name)
	}
	if events[0].Time.IsZero() {
		t.Error("expected non-zero event time")
	}

	span.End()
}

func TestSpanRecordError(t *testing.T) {
	tracer := NewTracer(TracerConfig{})

	_, span := tracer.Start(context.Background(), "test")

	err := errors.New("test error")
	span.RecordError(err)

	events := span.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	if events[0].Name != "exception" {
		t.Errorf("expected event name 'exception', got %q", events[0].Name)
	}

	status, msg := span.Status()
	if status != StatusError {
		t.Error("expected error status")
	}
	if msg != "test error" {
		t.Errorf("expected error message, got %q", msg)
	}

	span.End()
}

func TestSpanKind(t *testing.T) {
	tracer := NewTracer(TracerConfig{})

	_, span := tracer.Start(context.Background(), "test",
		WithSpanKind(SpanKindServer),
	)
	defer span.End()

	if span.Kind() != SpanKindServer {
		t.Errorf("expected SpanKindServer, got %v", span.Kind())
	}
}

func TestSpanDuration(t *testing.T) {
	tracer := NewTracer(TracerConfig{})

	_, span := tracer.Start(context.Background(), "test")

	time.Sleep(10 * time.Millisecond)

	duration := span.Duration()
	if duration < 10*time.Millisecond {
		t.Errorf("expected duration >= 10ms, got %v", duration)
	}

	span.End()
}

func TestAlwaysSampler(t *testing.T) {
	sampler := AlwaysSampler{}
	result := sampler.ShouldSample([16]byte{}, "test", false)

	if result.Decision != SamplingDecisionRecordAndSample {
		t.Error("AlwaysSampler should always sample")
	}
}

func TestNeverSampler(t *testing.T) {
	sampler := NeverSampler{}
	result := sampler.ShouldSample([16]byte{}, "test", false)

	if result.Decision != SamplingDecisionDrop {
		t.Error("NeverSampler should never sample")
	}
}

func TestRatioSampler(t *testing.T) {
	// Test 100% sampling
	sampler := NewRatioSampler(1.0)
	result := sampler.ShouldSample([16]byte{}, "test", false)
	if result.Decision != SamplingDecisionRecordAndSample {
		t.Error("100% ratio should always sample")
	}

	// Test 0% sampling
	sampler = NewRatioSampler(0.0)
	result = sampler.ShouldSample([16]byte{}, "test", false)
	if result.Decision != SamplingDecisionDrop {
		t.Error("0% ratio should never sample")
	}
}

func TestParentBasedSampler(t *testing.T) {
	sampler := NewParentBasedSampler(NeverSampler{})

	// With sampled parent
	result := sampler.ShouldSample([16]byte{}, "test", true)
	if result.Decision != SamplingDecisionRecordAndSample {
		t.Error("should sample when parent is sampled")
	}

	// Without parent (uses root sampler)
	result = sampler.ShouldSample([16]byte{}, "test", false)
	if result.Decision != SamplingDecisionDrop {
		t.Error("should not sample when no parent and root says no")
	}
}

func TestSpanContext(t *testing.T) {
	sc := SpanContext{}
	if sc.IsValid() {
		t.Error("empty span context should not be valid")
	}

	sc.TraceID = [16]byte{1, 2, 3}
	sc.SpanID = [8]byte{1, 2}
	if !sc.IsValid() {
		t.Error("span context with IDs should be valid")
	}
}
