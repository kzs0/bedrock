package trace

import (
	"context"
	"testing"
)

func TestDroppedSpanContextIsUnsampled(t *testing.T) {
	tr := NewTracer(TracerConfig{Sampler: NeverSampler{}})
	ctx, span := tr.Start(context.Background(), "dropped")
	sc := SpanContextFromContext(ctx)
	if !sc.IsValid() || sc.Sampled || span.IsRecording() {
		t.Fatalf("invalid dropped context: %+v", sc)
	}
}
func TestParentBasedSamplingPreservesDroppedLocalParent(t *testing.T) {
	parentTracer := NewTracer(TracerConfig{Sampler: NeverSampler{}})
	ctx, parent := parentTracer.Start(context.Background(), "parent")
	tr := NewTracer(TracerConfig{Sampler: NewParentBasedSampler(AlwaysSampler{})})
	_, child := tr.Start(ctx, "child")
	defer child.End()
	if child.IsRecording() {
		t.Fatal("dropped parent gained a sampled child")
	}
	if child.TraceID() != parent.TraceID() || child.ParentID() != parent.SpanID() {
		t.Fatal("child lost parent identity")
	}
}
func TestParentBasedSamplingPreservesUnsampledRemoteParent(t *testing.T) {
	root := NewTracer(TracerConfig{})
	ctx, parent := root.Start(context.Background(), "upstream")
	defer parent.End()
	remote := SpanContextFromContext(ctx)
	remote.Sampled = false
	remote.IsRemote = true
	remote.Tracestate = "vendor=value"
	tr := NewTracer(TracerConfig{Sampler: NewParentBasedSampler(AlwaysSampler{})})
	ctx, child := tr.Start(context.Background(), "child", WithRemoteParent(remote))
	defer child.End()
	if child.IsRecording() {
		t.Fatal("unsampled remote parent gained a sampled child")
	}
	sc := SpanContextFromContext(ctx)
	if sc.TraceID != parent.TraceID() || sc.Tracestate != remote.Tracestate {
		t.Fatal("dropped remote context lost propagation fields")
	}
}
func TestParentBasedSamplerStillSamplesRoots(t *testing.T) {
	tr := NewTracer(TracerConfig{Sampler: NewParentBasedSampler(AlwaysSampler{})})
	_, span := tr.Start(context.Background(), "root")
	defer span.End()
	if !span.IsRecording() {
		t.Fatal("root sampler was not consulted")
	}
}
