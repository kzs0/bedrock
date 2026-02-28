package otlp_test

import (
	"context"
	"testing"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace"
	"github.com/kzs0/bedrock/trace/otlp"
)

func makeSpan(t testing.TB, tracer *trace.Tracer, name string, attrs ...attr.Attr) *trace.Span {
	t.Helper()
	_, span := tracer.StartSpan(context.Background(), name, nil, nil, attrs)
	span.End()
	return span
}

func BenchmarkProtoEncodeSpans_Single(b *testing.B) {
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "bench"})
	span := makeSpan(b, tracer, "op")
	resource := attr.NewSet(attr.String("host", "localhost"))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = otlp.ProtoEncodeSpans([]*trace.Span{span}, "bench-svc", resource)
	}
}

func BenchmarkProtoEncodeSpans_Ten(b *testing.B) {
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "bench"})
	spans := make([]*trace.Span, 10)
	for i := range spans {
		spans[i] = makeSpan(b, tracer, "op")
	}
	resource := attr.NewSet()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = otlp.ProtoEncodeSpans(spans, "bench-svc", resource)
	}
}

func BenchmarkProtoEncodeSpans_WithAttrs(b *testing.B) {
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "bench"})
	span := makeSpan(b, tracer, "op",
		attr.String("http.method", "GET"),
		attr.String("http.route", "/users"),
		attr.Int("http.status_code", 200),
		attr.String("user_agent", "bench/1.0"),
	)
	resource := attr.NewSet(
		attr.String("host.name", "bench-host"),
		attr.String("deployment.environment", "prod"),
	)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = otlp.ProtoEncodeSpans([]*trace.Span{span}, "bench-svc", resource)
	}
}
