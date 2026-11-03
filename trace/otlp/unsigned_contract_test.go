package otlp

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace"
)

func unsignedMessage(t *testing.T, raw []byte, number int) []byte {
	t.Helper()
	for _, field := range boundaryFields(t, raw) {
		if field.number == number {
			return field.data
		}
	}
	t.Fatalf("protobuf message field %d missing", number)
	return nil
}

func unsignedAttribute(t *testing.T, raw []byte, number int) any {
	t.Helper()
	for _, field := range boundaryFields(t, raw) {
		if field.number != number {
			continue
		}
		kv := boundaryFields(t, field.data)
		if len(kv) == 2 && string(kv[0].data) == "unsigned" {
			return boundaryAny(t, kv[1].data)
		}
	}
	t.Fatal("unsigned attribute missing")
	return nil
}

func TestUnsignedTraceAttributesPreserveMagnitude(t *testing.T) {
	for _, value := range []uint64{0, 42, math.MaxInt64 - 1, math.MaxInt64, math.MaxInt64 + 1, math.MaxUint64} {
		t.Run(strconv.FormatUint(value, 10), func(t *testing.T) {
			tracer := trace.NewTracer(trace.TracerConfig{})
			_, span := tracer.Start(context.Background(), "unsigned.contract")
			span.SetAttr(attr.Uint64("unsigned", value))
			span.AddEvent("unsigned.event", attr.Uint64("unsigned", value))
			span.End()
			resource := attr.NewSet(attr.Uint64("unsigned", value))
			data, err := EncodeSpans([]*trace.Span{span}, "unsigned-service", resource)
			if err != nil {
				t.Fatal(err)
			}
			var decoded ExportRequest
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			r := decoded.ResourceSpans[0]
			s := r.ScopeSpans[0].Spans[0]
			for _, attrs := range [][]KeyValue{r.Resource.Attributes, s.Attributes, s.Events[0].Attributes} {
				var found bool
				for _, a := range attrs {
					if a.Key != "unsigned" {
						continue
					}
					found = true
					if value > math.MaxInt64 {
						if a.Value.StringValue == nil || *a.Value.StringValue != strconv.FormatUint(value, 10) || a.Value.IntValue != nil {
							t.Fatalf("unsigned magnitude lost in JSON: %#v", a.Value)
						}
					} else if a.Value.IntValue == nil || *a.Value.IntValue != int64(value) || a.Value.StringValue != nil {
						t.Fatalf("representable unsigned lost integer type: %#v", a.Value)
					}
				}
				if !found {
					t.Fatal("JSON unsigned attribute missing")
				}
			}
			data, err = ProtoEncodeSpans([]*trace.Span{span}, "unsigned-service", resource)
			if err != nil {
				t.Fatal(err)
			}
			rawResource := unsignedMessage(t, data, 1)
			rawSpan := unsignedMessage(t, unsignedMessage(t, rawResource, 2), 2)
			var want any = int64(value)
			if value > math.MaxInt64 {
				want = strconv.FormatUint(value, 10)
			}
			for name, got := range map[string]any{
				"resource": unsignedAttribute(t, unsignedMessage(t, rawResource, 1), 1),
				"span":     unsignedAttribute(t, rawSpan, 9),
				"event":    unsignedAttribute(t, unsignedMessage(t, rawSpan, 11), 3),
			} {
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s protobuf value=%#v, want %#v", name, got, want)
				}
			}
		})
	}
}

func TestUnsignedProfileValuesPreserveNestedMagnitude(t *testing.T) {
	for _, value := range []uint64{0, 42, math.MaxInt64, math.MaxInt64 + 1, math.MaxUint64} {
		var scalar any = int64(value)
		if value > math.MaxInt64 {
			scalar = strconv.FormatUint(value, 10)
		}
		for _, tc := range []struct{ input, want any }{
			{value, scalar}, {[]uint64{value}, []any{scalar}},
			{map[string]any{"nested": []any{value}}, map[string]any{"nested": []any{scalar}}},
		} {
			var b protoBuf
			budget := profileValueBudget{nodes: 4096, bytes: maxProfileBytes}
			if err := appendProfileValue(&b, 2, tc.input, 0, &budget); err != nil {
				t.Fatal(err)
			}
			got := boundaryAny(t, unsignedMessage(t, b.data, 2))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("profile value=%#v, want %#v", got, tc.want)
			}
		}
	}
}

func TestProtoAttributeDefaultsRetainOneofTypes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input attr.Value
		want  any
	}{
		{"string", attr.StringValue(""), ""}, {"integer", attr.Int64Value(0), int64(0)},
		{"unsigned", attr.Uint64Value(0), int64(0)}, {"float", attr.Float64Value(0), float64(0)},
		{"negative zero", attr.Float64Value(math.Copysign(0, -1)), math.Copysign(0, -1)},
		{"boolean", attr.BoolValue(false), false}, {"duration", attr.DurationValue(time.Duration(0)), int64(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b protoBuf
			b.appendAV(2, tc.input)
			got := boundaryAny(t, unsignedMessage(t, b.data, 2))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("default scalar=%#v, want %#v", got, tc.want)
			}
			if want, ok := tc.want.(float64); ok && math.Float64bits(got.(float64)) != math.Float64bits(want) {
				t.Fatal("floating-point sign bit changed")
			}
		})
	}
}
