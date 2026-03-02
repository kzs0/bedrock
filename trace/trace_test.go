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

// ── Additional span tests ─────────────────────────────────────────────────────

func TestSetAttrSet(t *testing.T) {
	tracer := NewTracer(TracerConfig{})
	_, span := tracer.Start(context.Background(), "test")
	defer span.End()

	set := attr.NewSet(
		attr.String("a", "1"),
		attr.Int("b", 2),
	)
	span.SetAttrSet(set)

	got := span.Attrs()
	if got.Len() != 2 {
		t.Errorf("expected 2 attrs, got %d", got.Len())
	}
	v, ok := got.Get("a")
	if !ok || v.AsString() != "1" {
		t.Error("expected attr a=1")
	}
}

func TestSetAttrSet_IgnoredAfterEnd(t *testing.T) {
	tracer := NewTracer(TracerConfig{})
	_, span := tracer.Start(context.Background(), "test")
	span.End()

	// SetAttrSet after End should be a no-op
	span.SetAttrSet(attr.NewSet(attr.String("x", "y")))
	if span.Attrs().Len() != 0 {
		t.Errorf("SetAttrSet after End should be ignored, got %d attrs", span.Attrs().Len())
	}
}

func TestSetStatus_OK(t *testing.T) {
	tracer := NewTracer(TracerConfig{})
	_, span := tracer.Start(context.Background(), "test")
	defer span.End()

	span.SetStatus(StatusOK, "all good")
	status, msg := span.Status()
	if status != StatusOK {
		t.Errorf("status: got %v, want StatusOK", status)
	}
	if msg != "all good" {
		t.Errorf("msg: got %q, want 'all good'", msg)
	}
}

func TestSetStatus_Error(t *testing.T) {
	tracer := NewTracer(TracerConfig{})
	_, span := tracer.Start(context.Background(), "test")
	defer span.End()

	span.SetStatus(StatusError, "something failed")
	status, msg := span.Status()
	if status != StatusError {
		t.Errorf("status: got %v, want StatusError", status)
	}
	if msg != "something failed" {
		t.Errorf("msg: got %q", msg)
	}
}

func TestSetStatus_IgnoredAfterEnd(t *testing.T) {
	tracer := NewTracer(TracerConfig{})
	_, span := tracer.Start(context.Background(), "test")
	span.End()

	span.SetStatus(StatusError, "too late")
	status, _ := span.Status()
	if status == StatusError {
		t.Error("SetStatus after End should be ignored")
	}
}

func TestIsRecording(t *testing.T) {
	tracer := NewTracer(TracerConfig{})
	_, span := tracer.Start(context.Background(), "test")

	if !span.IsRecording() {
		t.Error("span should be recording before End")
	}
	span.End()
	if span.IsRecording() {
		t.Error("span should not be recording after End")
	}
}

func TestSetAttr_IgnoredAfterEnd(t *testing.T) {
	tracer := NewTracer(TracerConfig{})
	_, span := tracer.Start(context.Background(), "test",
		WithAttrs(attr.String("initial", "value")),
	)
	span.End()
	span.SetAttr(attr.String("extra", "late"))

	attrs := span.Attrs()
	if _, ok := attrs.Get("extra"); ok {
		t.Error("SetAttr after End should be ignored")
	}
}

func TestAddEvent_IgnoredAfterEnd(t *testing.T) {
	tracer := NewTracer(TracerConfig{})
	_, span := tracer.Start(context.Background(), "test")
	span.End()

	span.AddEvent("late event")
	if len(span.Events()) != 0 {
		t.Error("AddEvent after End should be ignored")
	}
}

func TestRecordError_Nil(t *testing.T) {
	tracer := NewTracer(TracerConfig{})
	_, span := tracer.Start(context.Background(), "test")
	defer span.End()

	// nil error should be a no-op
	span.RecordError(nil)
	if len(span.Events()) != 0 {
		t.Error("RecordError(nil) should not add events")
	}
	status, _ := span.Status()
	if status == StatusError {
		t.Error("RecordError(nil) should not set error status")
	}
}

func TestRecordError_WithAttrs(t *testing.T) {
	tracer := NewTracer(TracerConfig{})
	_, span := tracer.Start(context.Background(), "test")
	defer span.End()

	span.RecordError(errors.New("oops"), attr.String("ctx", "extra"))
	events := span.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	v, ok := events[0].Attrs.Get("ctx")
	if !ok || v.AsString() != "extra" {
		t.Error("expected extra attr in error event")
	}
}

func TestSpanDuration_AfterEnd(t *testing.T) {
	tracer := NewTracer(TracerConfig{})
	_, span := tracer.Start(context.Background(), "test")
	time.Sleep(10 * time.Millisecond)
	span.End()

	d1 := span.Duration()
	time.Sleep(10 * time.Millisecond)
	d2 := span.Duration()

	if d1 < 10*time.Millisecond {
		t.Errorf("duration after End: got %v, want >= 10ms", d1)
	}
	if d1 != d2 {
		t.Errorf("duration should be fixed after End: d1=%v d2=%v", d1, d2)
	}
}

func TestSpanEndIdempotent(t *testing.T) {
	tracer := NewTracer(TracerConfig{})
	_, span := tracer.Start(context.Background(), "test")
	span.End()
	endTime1 := span.EndTime()
	time.Sleep(5 * time.Millisecond)
	span.End() // second call should be no-op
	endTime2 := span.EndTime()
	if endTime1 != endTime2 {
		t.Error("End should be idempotent; end time changed on second call")
	}
}

// ── Additional sampler tests ──────────────────────────────────────────────────

func TestRatioSampler_Clamping(t *testing.T) {
	s := NewRatioSampler(-0.5)
	// Negative ratio should be clamped to 0 (never sample)
	for i := 0; i < 100; i++ {
		r := s.ShouldSample([16]byte{byte(i)}, "op", false)
		if r.Decision != SamplingDecisionDrop {
			t.Errorf("negative ratio should be clamped to 0, got sample at i=%d", i)
		}
	}

	s2 := NewRatioSampler(1.5)
	// Ratio > 1 should be clamped to 1 (always sample)
	for i := 0; i < 100; i++ {
		r := s2.ShouldSample([16]byte{byte(i)}, "op", false)
		if r.Decision != SamplingDecisionRecordAndSample {
			t.Errorf("ratio > 1 should be clamped to 1, got drop at i=%d", i)
		}
	}
}

func TestRatioSampler_Statistical(t *testing.T) {
	s := NewRatioSampler(0.5)
	sampled := 0
	n := 1000
	for i := 0; i < n; i++ {
		var id [16]byte
		id[0] = byte(i)
		id[1] = byte(i >> 8)
		r := s.ShouldSample(id, "op", false)
		if r.Decision == SamplingDecisionRecordAndSample {
			sampled++
		}
	}
	// 50% ± 10% tolerance
	if sampled < 400 || sampled > 600 {
		t.Errorf("RatioSampler(0.5): sampled %d/%d, expected 400-600", sampled, n)
	}
}

func TestParentBasedSampler_AlwaysRoot(t *testing.T) {
	s := NewParentBasedSampler(AlwaysSampler{})

	// No parent (parentSampled=false) → uses root sampler (Always)
	r := s.ShouldSample([16]byte{}, "op", false)
	if r.Decision != SamplingDecisionRecordAndSample {
		t.Error("no parent + AlwaysSampler root should sample")
	}
}

func TestParentBasedSampler_NilRoot(t *testing.T) {
	s := NewParentBasedSampler(nil)

	// No parent, nil root → drop
	r := s.ShouldSample([16]byte{}, "op", false)
	if r.Decision != SamplingDecisionDrop {
		t.Error("no parent + nil root should drop")
	}

	// Sampled parent → always sample regardless of root
	r = s.ShouldSample([16]byte{}, "op", true)
	if r.Decision != SamplingDecisionRecordAndSample {
		t.Error("sampled parent should override nil root")
	}
}
