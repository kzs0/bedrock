package otlp

import (
	"context"
	"testing"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace"
)

// ── statusToOTLPProto ──────────────────────────────────────────────────────

func TestStatusToOTLPProto(t *testing.T) {
	tests := []struct {
		status trace.SpanStatus
		want   int
	}{
		{trace.StatusOK, 1},
		{trace.StatusError, 2},
		{trace.StatusUnset, 0},
		{trace.SpanStatus(99), 0}, // Unknown
	}
	for _, tt := range tests {
		got := statusToOTLPProto(tt.status)
		if got != tt.want {
			t.Errorf("statusToOTLPProto(%d) = %d, want %d", tt.status, got, tt.want)
		}
	}
}

// ── spanKindToOTLPProto ─────────────────────────────────────────────────────

func TestSpanKindToOTLPProto(t *testing.T) {
	tests := []struct {
		kind trace.SpanKind
		want int
	}{
		{trace.SpanKindInternal, 1},
		{trace.SpanKindServer, 2},
		{trace.SpanKindClient, 3},
		{trace.SpanKindProducer, 4},
		{trace.SpanKindConsumer, 5},
		{trace.SpanKind(99), 0}, // Unknown
	}
	for _, tt := range tests {
		got := spanKindToOTLPProto(tt.kind)
		if got != tt.want {
			t.Errorf("spanKindToOTLPProto(%d) = %d, want %d", tt.kind, got, tt.want)
		}
	}
}

// ── appendSpan with various span configurations ────────────────────────────

func TestProtoEncodeSpans_SpanWithError(t *testing.T) {
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.StartSpan(context.Background(), "error-test", nil, nil, nil)
	span.SetStatus(trace.StatusError, "something went wrong")
	span.End()

	data, err := ProtoEncodeSpans([]*trace.Span{span}, "svc", attr.NewSet())
	if err != nil {
		t.Fatalf("ProtoEncodeSpans error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty output")
	}
}

func TestProtoEncodeSpans_SpanWithEvents(t *testing.T) {
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.StartSpan(context.Background(), "event-test", nil, nil, nil)
	span.AddEvent("event1", attr.String("detail", "info"))
	span.AddEvent("event2", attr.Int("count", 3))
	span.End()

	data, err := ProtoEncodeSpans([]*trace.Span{span}, "svc", attr.NewSet())
	if err != nil {
		t.Fatalf("ProtoEncodeSpans error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty output")
	}
}

func TestProtoEncodeSpans_AllSpanKinds(t *testing.T) {
	kinds := []trace.SpanKind{
		trace.SpanKindInternal,
		trace.SpanKindServer,
		trace.SpanKindClient,
		trace.SpanKindProducer,
		trace.SpanKindConsumer,
	}

	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	for _, kind := range kinds {
		_, span := tracer.Start(context.Background(), "kind-test",
			trace.WithSpanKind(kind),
		)
		span.End()

		data, err := ProtoEncodeSpans([]*trace.Span{span}, "svc", attr.NewSet())
		if err != nil {
			t.Fatalf("ProtoEncodeSpans error for kind %d: %v", kind, err)
		}
		if len(data) == 0 {
			t.Errorf("expected non-empty output for kind %d", kind)
		}
	}
}

func TestProtoEncodeSpans_WithResourceAttrs(t *testing.T) {
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.StartSpan(context.Background(), "resource-test", nil, nil, nil)
	span.End()

	res := attr.NewSet(
		attr.String("env", "prod"),
		attr.String("region", "us-west-2"),
	)

	data, err := ProtoEncodeSpans([]*trace.Span{span}, "my-service", res)
	if err != nil {
		t.Fatalf("ProtoEncodeSpans error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty output")
	}
}

func TestProtoEncodeSpans_BoolAndFloat64Attrs(t *testing.T) {
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.StartSpan(context.Background(), "attr-test", nil, nil, nil)
	span.SetAttr(
		attr.Bool("enabled", true),
		attr.Float64("ratio", 0.75),
		attr.Uint64("uint_val", 999),
	)
	span.End()

	data, err := ProtoEncodeSpans([]*trace.Span{span}, "svc", attr.NewSet())
	if err != nil {
		t.Fatalf("ProtoEncodeSpans error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty output")
	}
}

// ── appendVarintField / appendFixed64 / appendDouble partial coverage ──────

func TestProtoEncodeSpans_LargeVarintValues(t *testing.T) {
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.StartSpan(context.Background(), "large-varint", nil, nil, nil)
	span.SetAttr(attr.Int("big", 1<<31))
	span.End()

	data, err := ProtoEncodeSpans([]*trace.Span{span}, "svc", attr.NewSet())
	if err != nil {
		t.Fatalf("ProtoEncodeSpans error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty output")
	}
}
