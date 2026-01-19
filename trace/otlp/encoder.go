package otlp

import (
	"encoding/json"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace"
)

// ExportRequest represents an OTLP trace export request.
type ExportRequest struct {
	ResourceSpans []ResourceSpans `json:"resourceSpans"`
}

// ResourceSpans groups spans by resource.
type ResourceSpans struct {
	Resource   Resource     `json:"resource"`
	ScopeSpans []ScopeSpans `json:"scopeSpans"`
}

// Resource represents a resource with attributes.
type Resource struct {
	Attributes []KeyValue `json:"attributes"`
}

// ScopeSpans groups spans by instrumentation scope.
type ScopeSpans struct {
	Scope InstrumentationScope `json:"scope"`
	Spans []Span               `json:"spans"`
}

// InstrumentationScope identifies the instrumentation library.
type InstrumentationScope struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// Span represents an OTLP span.
type Span struct {
	TraceID           string     `json:"traceId"`
	SpanID            string     `json:"spanId"`
	ParentSpanID      string     `json:"parentSpanId,omitempty"`
	Name              string     `json:"name"`
	Kind              int        `json:"kind"`
	StartTimeUnixNano uint64     `json:"startTimeUnixNano,string"`
	EndTimeUnixNano   uint64     `json:"endTimeUnixNano,string"`
	Attributes        []KeyValue `json:"attributes,omitempty"`
	Events            []Event    `json:"events,omitempty"`
	Status            Status     `json:"status,omitempty"`
}

// KeyValue represents a key-value attribute.
type KeyValue struct {
	Key   string   `json:"key"`
	Value AnyValue `json:"value"`
}

// AnyValue represents any attribute value.
type AnyValue struct {
	StringValue *string  `json:"stringValue,omitempty"`
	IntValue    *int64   `json:"intValue,string,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
}

// Event represents a span event.
type Event struct {
	TimeUnixNano uint64     `json:"timeUnixNano,string"`
	Name         string     `json:"name"`
	Attributes   []KeyValue `json:"attributes,omitempty"`
}

// Status represents the span status.
type Status struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// EncodeSpans encodes spans to OTLP JSON format.
func EncodeSpans(spans []*trace.Span, serviceName string, resource attr.Set) ([]byte, error) {
	if len(spans) == 0 {
		return nil, nil
	}

	// Build resource attributes
	resourceAttrs := []KeyValue{
		{Key: "service.name", Value: stringValue(serviceName)},
	}
	resource.Range(func(a attr.Attr) bool {
		resourceAttrs = append(resourceAttrs, attrToKeyValue(a))
		return true
	})

	// Convert spans
	otlpSpans := make([]Span, len(spans))
	for i, s := range spans {
		otlpSpans[i] = spanToOTLP(s)
	}

	request := ExportRequest{
		ResourceSpans: []ResourceSpans{
			{
				Resource: Resource{
					Attributes: resourceAttrs,
				},
				ScopeSpans: []ScopeSpans{
					{
						Scope: InstrumentationScope{
							Name:    "bedrock",
							Version: "1.0.0",
						},
						Spans: otlpSpans,
					},
				},
			},
		},
	}

	return json.Marshal(request)
}

// spanToOTLP converts a trace.Span to an OTLP Span.
func spanToOTLP(s *trace.Span) Span {
	otlpSpan := Span{
		TraceID:           s.TraceID().String(),
		SpanID:            s.SpanID().String(),
		Name:              s.Name(),
		Kind:              spanKindToOTLP(s.Kind()),
		StartTimeUnixNano: uint64(s.StartTime().UnixNano()),
		EndTimeUnixNano:   uint64(s.EndTime().UnixNano()),
	}

	if !s.ParentID().IsZero() {
		otlpSpan.ParentSpanID = s.ParentID().String()
	}

	// Convert attributes
	s.Attrs().Range(func(a attr.Attr) bool {
		otlpSpan.Attributes = append(otlpSpan.Attributes, attrToKeyValue(a))
		return true
	})

	// Convert events
	for _, e := range s.Events() {
		otlpEvent := Event{
			TimeUnixNano: uint64(e.Time.UnixNano()),
			Name:         e.Name,
		}
		e.Attrs.Range(func(a attr.Attr) bool {
			otlpEvent.Attributes = append(otlpEvent.Attributes, attrToKeyValue(a))
			return true
		})
		otlpSpan.Events = append(otlpSpan.Events, otlpEvent)
	}

	// Convert status
	status, msg := s.Status()
	if status != trace.StatusUnset {
		otlpSpan.Status = Status{
			Code:    statusToOTLP(status),
			Message: msg,
		}
	}

	return otlpSpan
}

// spanKindToOTLP converts SpanKind to OTLP kind.
func spanKindToOTLP(kind trace.SpanKind) int {
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

// statusToOTLP converts SpanStatus to OTLP status code.
func statusToOTLP(status trace.SpanStatus) int {
	switch status {
	case trace.StatusOK:
		return 1
	case trace.StatusError:
		return 2
	default:
		return 0
	}
}

// attrToKeyValue converts an attr.Attr to an OTLP KeyValue.
func attrToKeyValue(a attr.Attr) KeyValue {
	return KeyValue{
		Key:   a.Key,
		Value: valueToAnyValue(a.Value),
	}
}

// valueToAnyValue converts an attr.Value to an OTLP AnyValue.
func valueToAnyValue(v attr.Value) AnyValue {
	switch v.Kind() {
	case attr.KindString:
		s := v.AsString()
		return AnyValue{StringValue: &s}
	case attr.KindInt64:
		i := v.AsInt64()
		return AnyValue{IntValue: &i}
	case attr.KindUint64:
		i := int64(v.AsUint64())
		return AnyValue{IntValue: &i}
	case attr.KindFloat64:
		f := v.AsFloat64()
		return AnyValue{DoubleValue: &f}
	case attr.KindBool:
		b := v.AsBool()
		return AnyValue{BoolValue: &b}
	case attr.KindDuration:
		i := int64(v.AsDuration())
		return AnyValue{IntValue: &i}
	case attr.KindTime:
		s := v.AsTime().Format("2006-01-02T15:04:05.999999999Z07:00")
		return AnyValue{StringValue: &s}
	default:
		s := v.String()
		return AnyValue{StringValue: &s}
	}
}

// stringValue creates an AnyValue from a string.
func stringValue(s string) AnyValue {
	return AnyValue{StringValue: &s}
}
