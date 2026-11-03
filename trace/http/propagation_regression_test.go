package http

import (
	"context"
	"github.com/kzs0/bedrock/trace"
	"net/http"
	"testing"
)

func TestUnsampledSpanPropagatesValidContext(t *testing.T) {
	tr := trace.NewTracer(trace.TracerConfig{Sampler: trace.NeverSampler{}})
	ctx, span := tr.Start(context.Background(), "drop")
	headers := make(http.Header)
	p := &Propagator{}
	if err := p.Inject(ctx, headers); err != nil {
		t.Fatal(err)
	}
	got, err := p.Extract(headers)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sampled || got.TraceID != span.TraceID() || got.SpanID != span.SpanID() {
		t.Fatalf("incorrect propagated context: %+v", got)
	}
}
func TestEndedSpanStillPropagatesIdentity(t *testing.T) {
	tr := trace.NewTracer(trace.TracerConfig{})
	ctx, span := tr.Start(context.Background(), "ended")
	span.End()
	headers := make(http.Header)
	p := &Propagator{}
	if err := p.Inject(ctx, headers); err != nil {
		t.Fatal(err)
	}
	got, err := p.Extract(headers)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Sampled || got.TraceID != span.TraceID() {
		t.Fatalf("ended span context changed: %+v", got)
	}
}
func TestInjectClearsUnrelatedTracestate(t *testing.T) {
	tr := trace.NewTracer(trace.TracerConfig{})
	ctx, span := tr.Start(context.Background(), "root")
	defer span.End()
	headers := http.Header{"Tracestate": []string{"unrelated=secret"}}
	if err := (&Propagator{}).Inject(ctx, headers); err != nil {
		t.Fatal(err)
	}
	if headers.Get("tracestate") != "" {
		t.Fatal("new trace retained unrelated tracestate")
	}
}
