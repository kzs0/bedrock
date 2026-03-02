package otlp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace"
)

func TestEncodeSpans_Empty(t *testing.T) {
	data, err := EncodeSpans(nil, "svc", attr.NewSet())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data != nil {
		t.Error("expected nil for empty spans")
	}
}

func TestEncodeSpans_SingleSpan(t *testing.T) {
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.Start(context.Background(), "test-op",
		trace.WithAttrs(attr.String("key", "value")),
		trace.WithSpanKind(trace.SpanKindServer),
	)
	span.End()

	data, err := EncodeSpans([]*trace.Span{span}, "test-service", attr.NewSet(
		attr.String("env", "test"),
	))
	if err != nil {
		t.Fatalf("EncodeSpans error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty JSON output")
	}

	// Verify JSON structure
	var req ExportRequest
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	if len(req.ResourceSpans) != 1 {
		t.Fatalf("expected 1 ResourceSpans, got %d", len(req.ResourceSpans))
	}

	rs := req.ResourceSpans[0]
	// Check service.name in resource
	found := false
	for _, kv := range rs.Resource.Attributes {
		if kv.Key == "service.name" && kv.Value.StringValue != nil && *kv.Value.StringValue == "test-service" {
			found = true
		}
	}
	if !found {
		t.Error("expected service.name in resource attributes")
	}

	if len(rs.ScopeSpans) != 1 || len(rs.ScopeSpans[0].Spans) != 1 {
		t.Fatal("expected 1 scope span with 1 span")
	}

	otlpSpan := rs.ScopeSpans[0].Spans[0]
	if otlpSpan.Name != "test-op" {
		t.Errorf("expected span name 'test-op', got %q", otlpSpan.Name)
	}
	if otlpSpan.Kind != 2 { // SpanKindServer = 2
		t.Errorf("expected kind 2 (server), got %d", otlpSpan.Kind)
	}
}

func TestSpanKindToOTLP(t *testing.T) {
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
		got := spanKindToOTLP(tt.kind)
		if got != tt.want {
			t.Errorf("spanKindToOTLP(%d) = %d, want %d", tt.kind, got, tt.want)
		}
	}
}

func TestStatusToOTLP(t *testing.T) {
	tests := []struct {
		status trace.SpanStatus
		want   int
	}{
		{trace.StatusOK, 1},
		{trace.StatusError, 2},
		{trace.StatusUnset, 0},
	}
	for _, tt := range tests {
		got := statusToOTLP(tt.status)
		if got != tt.want {
			t.Errorf("statusToOTLP(%d) = %d, want %d", tt.status, got, tt.want)
		}
	}
}

func TestValueToAnyValue_AllKinds(t *testing.T) {
	s := "hello"
	i := int64(42)
	f := 3.14
	b := true

	tests := []struct {
		name string
		v    attr.Value
		want AnyValue
	}{
		{"string", attr.StringValue("hello"), AnyValue{StringValue: &s}},
		{"int64", attr.Int64Value(42), AnyValue{IntValue: &i}},
		{"float64", attr.Float64Value(3.14), AnyValue{DoubleValue: &f}},
		{"bool", attr.BoolValue(true), AnyValue{BoolValue: &b}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := valueToAnyValue(tt.v)
			data1, _ := json.Marshal(got)
			data2, _ := json.Marshal(tt.want)
			if string(data1) != string(data2) {
				t.Errorf("got %s, want %s", data1, data2)
			}
		})
	}
}

func TestValueToAnyValue_Uint64(t *testing.T) {
	v := attr.Uint64Value(42)
	got := valueToAnyValue(v)
	if got.IntValue == nil || *got.IntValue != 42 {
		t.Error("expected int value 42 for uint64")
	}
}

func TestValueToAnyValue_Duration(t *testing.T) {
	v := attr.DurationValue(5 * time.Second)
	got := valueToAnyValue(v)
	if got.IntValue == nil {
		t.Error("expected int value for duration")
	}
}

func TestValueToAnyValue_Time(t *testing.T) {
	v := attr.TimeValue(time.Now())
	got := valueToAnyValue(v)
	if got.StringValue == nil {
		t.Error("expected string value for time")
	}
}

func TestStringValue(t *testing.T) {
	sv := stringValue("hello")
	if sv.StringValue == nil || *sv.StringValue != "hello" {
		t.Error("expected string value 'hello'")
	}
}

func TestAttrToKeyValue(t *testing.T) {
	a := attr.String("key", "value")
	kv := attrToKeyValue(a)
	if kv.Key != "key" {
		t.Errorf("expected key 'key', got %q", kv.Key)
	}
	if kv.Value.StringValue == nil || *kv.Value.StringValue != "value" {
		t.Error("expected string value 'value'")
	}
}

func TestEncodeSpans_WithEvents(t *testing.T) {
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.Start(context.Background(), "with-events")
	span.AddEvent("event1", attr.String("detail", "info"))
	span.End()

	data, err := EncodeSpans([]*trace.Span{span}, "svc", attr.NewSet())
	if err != nil {
		t.Fatalf("EncodeSpans error: %v", err)
	}

	var req ExportRequest
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	otlpSpan := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
	if len(otlpSpan.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(otlpSpan.Events))
	}
	if otlpSpan.Events[0].Name != "event1" {
		t.Errorf("expected event name 'event1', got %q", otlpSpan.Events[0].Name)
	}
}

func TestEncodeSpans_WithStatus(t *testing.T) {
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.Start(context.Background(), "with-error")
	span.SetStatus(trace.StatusError, "something went wrong")
	span.End()

	data, err := EncodeSpans([]*trace.Span{span}, "svc", attr.NewSet())
	if err != nil {
		t.Fatalf("EncodeSpans error: %v", err)
	}

	var req ExportRequest
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	otlpSpan := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
	if otlpSpan.Status.Code != 2 {
		t.Errorf("expected status code 2 (error), got %d", otlpSpan.Status.Code)
	}
	if otlpSpan.Status.Message != "something went wrong" {
		t.Errorf("expected status message, got %q", otlpSpan.Status.Message)
	}
}

func TestEncodeSpans_WithParent(t *testing.T) {
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, parent := tracer.Start(context.Background(), "parent")
	ctx := trace.ContextWithSpan(context.Background(), parent)
	_, child := tracer.Start(ctx, "child")
	parent.End()
	child.End()

	data, err := EncodeSpans([]*trace.Span{child}, "svc", attr.NewSet())
	if err != nil {
		t.Fatalf("EncodeSpans error: %v", err)
	}

	var req ExportRequest
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	otlpSpan := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
	if otlpSpan.ParentSpanID == "" {
		t.Error("expected non-empty parent span ID")
	}
}
