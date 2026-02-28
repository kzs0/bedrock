package otlp

import (
	"context"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/internal"
	"github.com/kzs0/bedrock/trace"
)

func TestProtoEncodeSpans_Empty(t *testing.T) {
	data, err := ProtoEncodeSpans(nil, "svc", attr.NewSet())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data != nil {
		t.Fatalf("expected nil for empty spans, got %d bytes", len(data))
	}
}

func TestProtoEncodeSpans_SingleSpan(t *testing.T) {
	// Create a span by exercising the tracer
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	ctx, span := tracer.StartSpan(context.Background(), "op", nil, nil, []attr.Attr{
		attr.String("key", "value"),
	})
	_ = ctx
	span.SetAttr(attr.String("extra", "attr"))
	span.End()

	data, err := ProtoEncodeSpans([]*trace.Span{span}, "test-service", attr.NewSet(
		attr.String("env", "test"),
	))
	if err != nil {
		t.Fatalf("ProtoEncodeSpans error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty protobuf output")
	}

	// Validate protobuf structure: first byte should be field 1 (resource_spans),
	// wire type 2 (length-delimited) → tag = (1<<3)|2 = 0x0a
	if data[0] != 0x0a {
		t.Errorf("expected first tag byte 0x0a, got 0x%02x", data[0])
	}
}

func TestProtoEncodeSpans_AllAttrKinds(t *testing.T) {
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.StartSpan(context.Background(), "attr-test", nil, nil, nil)
	span.SetAttr(
		attr.String("s", "hello"),
		attr.Int("i", 42),
		attr.Float64("f", 3.14),
		attr.Bool("b", true),
		attr.Duration("d", 5*time.Second),
	)
	span.End()

	data, err := ProtoEncodeSpans([]*trace.Span{span}, "svc", attr.NewSet())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty output")
	}
}

func TestProtoEncodeSpans_SpanWithParent(t *testing.T) {
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, parent := tracer.StartSpan(context.Background(), "parent", nil, nil, nil)
	_, child := tracer.StartSpan(context.Background(), "child", parent, nil, nil)
	parent.End()
	child.End()

	data, err := ProtoEncodeSpans([]*trace.Span{parent, child}, "svc", attr.NewSet())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty output")
	}
}

// Verify protoBuf varint encoding correctness.
func TestProtoBufVarint(t *testing.T) {
	tests := []struct {
		v    uint64
		want []byte
	}{
		{0, []byte{0x00}},
		{1, []byte{0x01}},
		{127, []byte{0x7f}},
		{128, []byte{0x80, 0x01}},
		{300, []byte{0xac, 0x02}},
		{16384, []byte{0x80, 0x80, 0x01}},
	}
	for _, tt := range tests {
		var b protoBuf
		b.appendRawVarint(tt.v)
		if string(b.data) != string(tt.want) {
			t.Errorf("varint(%d): got %v, want %v", tt.v, b.data, tt.want)
		}
	}
}

// Ensure TraceID bytes are embedded raw (not hex-encoded).
func TestProtoEncodeSpans_TraceIDBinary(t *testing.T) {
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.StartSpan(context.Background(), "trace-id-test", nil, nil, nil)
	span.End()

	data, err := ProtoEncodeSpans([]*trace.Span{span}, "svc", attr.NewSet())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// TraceID is 16 bytes; it must appear as raw bytes in the output,
	// not as 32-byte hex. Verify the bytes appear somewhere in the payload.
	traceIDBytes := span.TraceID()
	found := false
	for i := 0; i+16 <= len(data); i++ {
		if compareBytes(data[i:i+16], traceIDBytes[:]) {
			found = true
			break
		}
	}
	if !found {
		t.Error("raw TraceID bytes not found in protobuf output")
	}
}

func compareBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Silence unused import warning for internal package used by span.TraceID() return type.
var _ internal.TraceID
