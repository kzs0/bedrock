package otlp

import (
	"math"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace"
)

// ProtoEncodeSpans encodes spans to OTLP protobuf binary format
// (opentelemetry.proto.collector.trace.v1.ExportTraceServiceRequest).
//
// Field numbers from the OTLP protobuf schema:
//
//	ExportTraceServiceRequest.resource_spans = 1
//	ResourceSpans.resource = 1, .scope_spans = 2
//	Resource.attributes = 1
//	ScopeSpans.scope = 1, .spans = 2
//	InstrumentationScope.name = 1, .version = 2
//	Span.trace_id=1, span_id=2, trace_state=3, parent_span_id=4, name=5, kind=6,
//	     start_time_unix_nano=7, end_time_unix_nano=8, attributes=9,
//	     events=11, status=15
//	Event.time_unix_nano=1, name=2, attributes=3
//	Status.message=2, code=3
//	KeyValue.key=1, value=2
//	AnyValue: string_value=1, bool_value=2, int_value=3, double_value=4, bytes_value=7
//
// All encoding is done into a single growing buffer using a begin/end message
// pattern that backpatches length varints, eliminating per-field allocations.
func ProtoEncodeSpans(spans []*trace.Span, serviceName string, resource attr.Set) ([]byte, error) {
	if len(spans) == 0 {
		return nil, nil
	}

	// Estimate capacity: ~200 bytes per span + resource overhead.
	est := 200*len(spans) + 64 + len(serviceName)
	resource.Range(func(a attr.Attr) bool {
		est += len(a.Key) + 32
		return true
	})

	var b protoBuf
	b.data = make([]byte, 0, est)

	// ExportTraceServiceRequest.resource_spans (field 1)
	off0 := b.beginMsg(1)

	// ResourceSpans.resource (field 1)
	off1 := b.beginMsg(1)
	b.appendKV(1, "service.name", attr.StringValue(serviceName))
	resource.Range(func(a attr.Attr) bool {
		b.appendKV(1, a.Key, a.Value)
		return true
	})
	b.endMsg(off1)

	// ResourceSpans.scope_spans (field 2)
	off2 := b.beginMsg(2)

	// ScopeSpans.scope (InstrumentationScope, field 1)
	off3 := b.beginMsg(1)
	b.appendString(1, "bedrock")
	b.appendString(2, "1.0.0")
	b.endMsg(off3)

	// ScopeSpans.spans (field 2) — one entry per span
	for _, s := range spans {
		off4 := b.beginMsg(2)
		b.appendSpan(s)
		b.endMsg(off4)
	}
	b.endMsg(off2)

	b.endMsg(off0)
	return b.data, nil
}

// appendSpan writes all fields of a span into b.
func (b *protoBuf) appendSpan(s *trace.Span) {
	tid := s.TraceID()
	b.appendBytes(1, tid[:])

	sid := s.SpanID()
	b.appendBytes(2, sid[:])

	if !s.ParentID().IsZero() {
		pid := s.ParentID()
		b.appendBytes(4, pid[:])
	}

	b.appendString(5, s.Name())
	b.appendVarintField(6, uint64(spanKindToOTLPProto(s.Kind())))
	b.appendFixed64(7, uint64(s.StartTime().UnixNano()))
	b.appendFixed64(8, uint64(s.EndTime().UnixNano()))

	s.Attrs().Range(func(a attr.Attr) bool {
		b.appendKV(9, a.Key, a.Value)
		return true
	})

	for _, e := range s.Events() {
		off := b.beginMsg(11)
		b.appendFixed64(1, uint64(e.Time.UnixNano()))
		b.appendString(2, e.Name)
		e.Attrs.Range(func(a attr.Attr) bool {
			b.appendKV(3, a.Key, a.Value)
			return true
		})
		b.endMsg(off)
	}

	status, msg := s.Status()
	if status != trace.StatusUnset {
		off := b.beginMsg(15)
		if msg != "" {
			b.appendString(2, msg)
		}
		b.appendVarintField(3, uint64(statusToOTLPProto(status)))
		b.endMsg(off)
	}
}

// appendKV writes a KeyValue message at fieldNumber.
func (b *protoBuf) appendKV(fieldNumber int, key string, value attr.Value) {
	off := b.beginMsg(fieldNumber)
	b.appendString(1, key)
	b.appendAV(2, value)
	b.endMsg(off)
}

// appendAV writes an AnyValue message at fieldNumber.
func (b *protoBuf) appendAV(fieldNumber int, v attr.Value) {
	off := b.beginMsg(fieldNumber)
	switch v.Kind() {
	case attr.KindString:
		b.appendString(1, v.AsString())
	case attr.KindBool:
		if v.AsBool() {
			b.appendVarintField(2, 1)
		}
	case attr.KindInt64:
		b.appendVarintField(3, uint64(v.AsInt64()))
	case attr.KindUint64:
		b.appendVarintField(3, v.AsUint64())
	case attr.KindFloat64:
		b.appendDouble(4, v.AsFloat64())
	case attr.KindDuration:
		b.appendVarintField(3, uint64(v.AsDuration()))
	default:
		b.appendString(1, v.String())
	}
	b.endMsg(off)
}

func spanKindToOTLPProto(kind trace.SpanKind) int {
	switch kind {
	case trace.SpanKindInternal:
		return 1
	case trace.SpanKindServer:
		return 2
	case trace.SpanKindClient:
		return 3
	case trace.SpanKindProducer:
		return 4
	case trace.SpanKindConsumer:
		return 5
	default:
		return 0
	}
}

func statusToOTLPProto(status trace.SpanStatus) int {
	switch status {
	case trace.StatusOK:
		return 1
	case trace.StatusError:
		return 2
	default:
		return 0
	}
}

// protoBuf is a minimal protobuf binary encoder that writes all fields into a
// single growing buffer. Nested messages use beginMsg/endMsg for backpatched
// length varints, eliminating per-message buffer allocations.
//
// Wire types: 0=varint, 1=64-bit, 2=length-delimited.
type protoBuf struct {
	data []byte
}

// beginMsg writes a field tag (wire type 2) and reserves 5 bytes for the
// message length varint. Returns the offset of the reserved bytes so that
// endMsg can patch the actual length after the payload is written.
//
// Using a fixed 5-byte slot means we may need to shift the payload left when
// endMsg computes the real length and fits it in fewer bytes. The memmove is
// cheap (payload is typically <1 KB) and produces correct minimal encoding.
func (b *protoBuf) beginMsg(fieldNumber int) int {
	b.appendTag(fieldNumber, 2)
	off := len(b.data)
	b.data = append(b.data, 0, 0, 0, 0, 0) // 5-byte length placeholder
	return off
}

// endMsg patches the 5-byte placeholder at off with the minimal varint for the
// payload size, then shifts the payload left to close any unused placeholder bytes.
func (b *protoBuf) endMsg(off int) {
	size := len(b.data) - off - 5

	// Count bytes required for minimal varint encoding
	needed := 1
	v := uint64(size)
	for v >= 0x80 {
		needed++
		v >>= 7
	}

	// Write minimal varint into the first `needed` bytes of the placeholder
	v = uint64(size)
	for i := range needed - 1 {
		b.data[off+i] = byte(v) | 0x80
		v >>= 7
	}
	b.data[off+needed-1] = byte(v)

	// Close the unused trailing placeholder bytes by shifting the payload left
	if needed < 5 {
		copy(b.data[off+needed:], b.data[off+5:])
		b.data = b.data[:len(b.data)-(5-needed)]
	}
}

// appendTag encodes a field tag (field number + wire type) as a varint.
func (b *protoBuf) appendTag(fieldNumber int, wireType int) {
	b.appendRawVarint(uint64((fieldNumber << 3) | wireType))
}

// appendRawVarint encodes an unsigned integer as a base-128 varint.
func (b *protoBuf) appendRawVarint(v uint64) {
	for v >= 0x80 {
		b.data = append(b.data, byte(v)|0x80)
		v >>= 7
	}
	b.data = append(b.data, byte(v))
}

// appendVarintField encodes an int/enum field (wire type 0).
func (b *protoBuf) appendVarintField(fieldNumber int, v uint64) {
	if v == 0 {
		return // default value; omit per proto3
	}
	b.appendTag(fieldNumber, 0)
	b.appendRawVarint(v)
}

// appendFixed64 encodes a fixed64 field (wire type 1): 8 bytes little-endian.
func (b *protoBuf) appendFixed64(fieldNumber int, v uint64) {
	if v == 0 {
		return
	}
	b.appendTag(fieldNumber, 1)
	b.data = append(b.data,
		byte(v), byte(v>>8), byte(v>>16), byte(v>>24),
		byte(v>>32), byte(v>>40), byte(v>>48), byte(v>>56),
	)
}

// appendDouble encodes a double field (wire type 1): 8 bytes IEEE-754 little-endian.
func (b *protoBuf) appendDouble(fieldNumber int, v float64) {
	if v == 0 {
		return
	}
	b.appendFixed64(fieldNumber, math.Float64bits(v))
}

// appendBytes encodes a bytes field (wire type 2).
func (b *protoBuf) appendBytes(fieldNumber int, data []byte) {
	if len(data) == 0 {
		return
	}
	b.appendTag(fieldNumber, 2)
	b.appendRawVarint(uint64(len(data)))
	b.data = append(b.data, data...)
}

// appendString encodes a string field (wire type 2).
func (b *protoBuf) appendString(fieldNumber int, s string) {
	if s == "" {
		return
	}
	b.appendTag(fieldNumber, 2)
	b.appendRawVarint(uint64(len(s)))
	b.data = append(b.data, s...)
}
