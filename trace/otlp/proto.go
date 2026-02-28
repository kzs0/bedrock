package otlp

import (
	"encoding/binary"
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
func ProtoEncodeSpans(spans []*trace.Span, serviceName string, resource attr.Set) ([]byte, error) {
	if len(spans) == 0 {
		return nil, nil
	}

	// Build resource attributes
	var resourceBuf protoBuf
	resourceBuf.appendMessage(1, encodeResourceAttrs(serviceName, resource))

	// Build scope spans
	var scopeSpansBuf protoBuf
	// InstrumentationScope
	var scopeBuf protoBuf
	scopeBuf.appendString(1, "bedrock")
	scopeBuf.appendString(2, "1.0.0")
	scopeSpansBuf.appendMessage(1, scopeBuf.data)
	// Spans
	for _, s := range spans {
		scopeSpansBuf.appendMessage(2, encodeProtoSpan(s))
	}

	// Build ResourceSpans
	var rsBuf protoBuf
	rsBuf.appendMessage(1, resourceBuf.data)
	rsBuf.appendMessage(2, scopeSpansBuf.data)

	// Wrap in ExportTraceServiceRequest
	var req protoBuf
	req.appendMessage(1, rsBuf.data)
	return req.data, nil
}

func encodeResourceAttrs(serviceName string, resource attr.Set) []byte {
	var b protoBuf
	b.appendMessage(1, encodeKeyValueProto("service.name", attr.StringValue(serviceName)))
	resource.Range(func(a attr.Attr) bool {
		b.appendMessage(1, encodeKeyValueProto(a.Key, a.Value))
		return true
	})
	return b.data
}

func encodeProtoSpan(s *trace.Span) []byte {
	var b protoBuf

	// trace_id (bytes, field 1) - 16 raw bytes
	tid := s.TraceID()
	b.appendBytes(1, tid[:])

	// span_id (bytes, field 2) - 8 raw bytes
	sid := s.SpanID()
	b.appendBytes(2, sid[:])

	// parent_span_id (bytes, field 4) - 8 raw bytes, omit if zero
	if !s.ParentID().IsZero() {
		pid := s.ParentID()
		b.appendBytes(4, pid[:])
	}

	// name (string, field 5)
	b.appendString(5, s.Name())

	// kind (enum/int32, field 6)
	b.appendVarintField(6, uint64(spanKindToOTLPProto(s.Kind())))

	// start_time_unix_nano (fixed64, field 7)
	b.appendFixed64(7, uint64(s.StartTime().UnixNano()))

	// end_time_unix_nano (fixed64, field 8)
	b.appendFixed64(8, uint64(s.EndTime().UnixNano()))

	// attributes (repeated KeyValue, field 9)
	s.Attrs().Range(func(a attr.Attr) bool {
		b.appendMessage(9, encodeKeyValueProto(a.Key, a.Value))
		return true
	})

	// events (repeated Event, field 11)
	for _, e := range s.Events() {
		var ev protoBuf
		ev.appendFixed64(1, uint64(e.Time.UnixNano()))
		ev.appendString(2, e.Name)
		e.Attrs.Range(func(a attr.Attr) bool {
			ev.appendMessage(3, encodeKeyValueProto(a.Key, a.Value))
			return true
		})
		b.appendMessage(11, ev.data)
	}

	// status (message, field 15)
	status, msg := s.Status()
	if status != trace.StatusUnset {
		var st protoBuf
		if msg != "" {
			st.appendString(2, msg)
		}
		st.appendVarintField(3, uint64(statusToOTLPProto(status)))
		b.appendMessage(15, st.data)
	}

	return b.data
}

func encodeKeyValueProto(key string, value attr.Value) []byte {
	var b protoBuf
	b.appendString(1, key)
	b.appendMessage(2, encodeAnyValue(value))
	return b.data
}

func encodeAnyValue(v attr.Value) []byte {
	var b protoBuf
	switch v.Kind() {
	case attr.KindString:
		b.appendString(1, v.AsString())
	case attr.KindBool:
		val := uint64(0)
		if v.AsBool() {
			val = 1
		}
		b.appendVarintField(2, val)
	case attr.KindInt64:
		b.appendVarintField(3, uint64(v.AsInt64()))
	case attr.KindUint64:
		b.appendVarintField(3, v.AsUint64())
	case attr.KindFloat64:
		b.appendDouble(4, v.AsFloat64())
	case attr.KindDuration:
		b.appendVarintField(3, uint64(v.AsDuration()))
	default:
		s := v.String()
		b.appendString(1, s)
	}
	return b.data
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

// protoBuf is a minimal protobuf binary encoder.
// Wire types: 0=varint, 1=64-bit, 2=length-delimited, 5=32-bit.
type protoBuf struct {
	data []byte
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
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], v)
	b.data = append(b.data, tmp[:]...)
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

// appendMessage encodes an embedded message field (wire type 2) from pre-encoded bytes.
// An empty message is NOT omitted (may be needed for required fields in proto2,
// and for zero-value proto3 messages we still want to include).
func (b *protoBuf) appendMessage(fieldNumber int, msg []byte) {
	b.appendTag(fieldNumber, 2)
	b.appendRawVarint(uint64(len(msg)))
	b.data = append(b.data, msg...)
}
