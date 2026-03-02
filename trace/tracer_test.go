package trace

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/internal"
)

type mockExporter struct {
	calls atomic.Int32
	spans []*Span
}

func (m *mockExporter) ExportSpans(_ context.Context, spans []*Span) error {
	m.calls.Add(1)
	m.spans = append(m.spans, spans...)
	return nil
}

func (m *mockExporter) Shutdown(_ context.Context) error { return nil }

type mockEnqueuer struct {
	mockExporter
	enqueueCalls atomic.Int32
}

func (m *mockEnqueuer) EnqueueSpan(span *Span) {
	m.enqueueCalls.Add(1)
}

func TestTracer_StartSpan(t *testing.T) {
	tracer := NewTracer(TracerConfig{ServiceName: "test"})
	ctx, span := tracer.StartSpan(context.Background(), "op", nil, nil, nil)
	defer span.End()

	if span.Name() != "op" {
		t.Errorf("expected name 'op', got %q", span.Name())
	}
	if span.TraceID().IsZero() {
		t.Error("expected non-zero trace ID")
	}
	if span.StartTime().IsZero() {
		t.Error("expected non-zero start time")
	}
	// Should be in context
	fromCtx := SpanFromContext(ctx)
	if fromCtx != span {
		t.Error("span should be in context")
	}
}

func TestTracer_StartSpan_WithParent(t *testing.T) {
	tracer := NewTracer(TracerConfig{ServiceName: "test"})
	_, parent := tracer.StartSpan(context.Background(), "parent", nil, nil, nil)
	_, child := tracer.StartSpan(context.Background(), "child", parent, nil, nil)
	defer parent.End()
	defer child.End()

	if child.TraceID() != parent.TraceID() {
		t.Error("child should inherit parent trace ID")
	}
	if child.ParentID() != parent.SpanID() {
		t.Error("child parent ID should match parent span ID")
	}
}

func TestTracer_StartSpan_WithRemoteParent(t *testing.T) {
	tracer := NewTracer(TracerConfig{ServiceName: "test"})
	traceID := internal.NewTraceID()
	spanID := internal.NewSpanID()
	remote := &SpanContext{
		TraceID:    traceID,
		SpanID:     spanID,
		Tracestate: "vendor=value",
		IsRemote:   true,
		Sampled:    true,
	}

	_, span := tracer.StartSpan(context.Background(), "child", nil, remote, nil)
	defer span.End()

	if span.TraceID() != traceID {
		t.Error("should inherit remote parent trace ID")
	}
	if span.ParentID() != spanID {
		t.Error("parent ID should match remote parent span ID")
	}
}

func TestTracer_SetExporter(t *testing.T) {
	exp := &mockExporter{}
	tracer := NewTracer(TracerConfig{ServiceName: "test"})

	tracer.SetExporter(exp)

	_, span := tracer.Start(context.Background(), "test")
	span.End()

	// Give async export time to complete
	time.Sleep(50 * time.Millisecond)

	if exp.calls.Load() == 0 {
		t.Error("expected exporter to be called after SetExporter")
	}
}

func TestTracer_Shutdown(t *testing.T) {
	exp := &mockExporter{}
	tracer := NewTracer(TracerConfig{
		ServiceName: "test",
		Exporter:    exp,
	})

	err := tracer.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("Shutdown error: %v", err)
	}
}

func TestTracer_Shutdown_NoExporter(t *testing.T) {
	tracer := NewTracer(TracerConfig{ServiceName: "test"})
	err := tracer.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("Shutdown error: %v", err)
	}
}

func TestTracer_ServiceName(t *testing.T) {
	tracer := NewTracer(TracerConfig{ServiceName: "my-service"})
	if tracer.ServiceName() != "my-service" {
		t.Errorf("expected 'my-service', got %q", tracer.ServiceName())
	}
}

func TestTracer_Resource(t *testing.T) {
	res := attr.NewSet(attr.String("env", "prod"))
	tracer := NewTracer(TracerConfig{
		ServiceName: "test",
		Resource:    res,
	})

	got := tracer.Resource()
	v, ok := got.Get("env")
	if !ok || v.AsString() != "prod" {
		t.Error("expected resource attr env=prod")
	}
}

func TestTracer_WithParent_Option(t *testing.T) {
	tracer := NewTracer(TracerConfig{ServiceName: "test"})
	_, parent := tracer.Start(context.Background(), "parent")
	defer parent.End()

	// Use WithParent option to set explicit parent (without context)
	_, child := tracer.Start(context.Background(), "child", WithParent(parent))
	defer child.End()

	if child.TraceID() != parent.TraceID() {
		t.Error("child should inherit parent trace ID via WithParent")
	}
	if child.ParentID() != parent.SpanID() {
		t.Error("child parent ID should match via WithParent")
	}
}

func TestTracer_WithRemoteParent_Option(t *testing.T) {
	tracer := NewTracer(TracerConfig{ServiceName: "test"})
	traceID := internal.NewTraceID()
	spanID := internal.NewSpanID()
	remote := SpanContext{
		TraceID:  traceID,
		SpanID:   spanID,
		Sampled:  true,
		IsRemote: true,
	}

	_, span := tracer.Start(context.Background(), "child", WithRemoteParent(remote))
	defer span.End()

	if span.TraceID() != traceID {
		t.Error("should inherit remote parent trace ID")
	}
}

func TestTracer_Export_SpanEnqueuer(t *testing.T) {
	eq := &mockEnqueuer{}
	tracer := NewTracer(TracerConfig{
		ServiceName: "test",
		Exporter:    eq,
	})

	_, span := tracer.Start(context.Background(), "test")
	span.End()

	// SpanEnqueuer should be called synchronously
	if eq.enqueueCalls.Load() != 1 {
		t.Errorf("expected 1 EnqueueSpan call, got %d", eq.enqueueCalls.Load())
	}
}

func TestTracer_Export_AsyncExporter(t *testing.T) {
	exp := &mockExporter{}
	tracer := NewTracer(TracerConfig{
		ServiceName: "test",
		Exporter:    exp,
	})

	_, span := tracer.Start(context.Background(), "test")
	span.End()

	time.Sleep(50 * time.Millisecond)

	if exp.calls.Load() != 1 {
		t.Errorf("expected 1 ExportSpans call, got %d", exp.calls.Load())
	}
}

func TestTracer_Export_NilExporter(t *testing.T) {
	tracer := NewTracer(TracerConfig{ServiceName: "test"})
	_, span := tracer.Start(context.Background(), "test")
	// Should not panic
	span.End()
}

func TestSpan_StartTime(t *testing.T) {
	tracer := NewTracer(TracerConfig{ServiceName: "test"})
	before := time.Now()
	_, span := tracer.Start(context.Background(), "test")
	after := time.Now()
	defer span.End()

	st := span.StartTime()
	if st.Before(before) || st.After(after) {
		t.Errorf("start time %v not between %v and %v", st, before, after)
	}
}

func TestTracer_Start_RemoteParentTakesPrecedence(t *testing.T) {
	tracer := NewTracer(TracerConfig{ServiceName: "test"})

	// Create local parent
	ctx, localParent := tracer.Start(context.Background(), "local-parent")
	defer localParent.End()

	// Create remote parent
	remoteTraceID := internal.NewTraceID()
	remoteSpanID := internal.NewSpanID()
	remote := SpanContext{
		TraceID:  remoteTraceID,
		SpanID:   remoteSpanID,
		Sampled:  true,
		IsRemote: true,
	}

	// Start child with both local parent (in ctx) and remote parent
	_, child := tracer.Start(ctx, "child", WithRemoteParent(remote))
	defer child.End()

	// Remote parent should take precedence
	if child.TraceID() != remoteTraceID {
		t.Error("remote parent should take precedence over local parent")
	}
	if child.ParentID() != remoteSpanID {
		t.Error("parent ID should be from remote parent")
	}
}
