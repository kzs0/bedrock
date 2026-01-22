package http

import (
	"context"
	"net/http"
	"testing"

	"github.com/kzs0/bedrock/internal"
	"github.com/kzs0/bedrock/trace"
	"github.com/kzs0/bedrock/trace/w3c"
)

func TestPropagatorExtract(t *testing.T) {
	prop := &Propagator{}

	tests := []struct {
		name      string
		headers   http.Header
		wantErr   bool
		checkFunc func(t *testing.T, sc trace.SpanContext)
	}{
		{
			name: "valid traceparent only",
			headers: http.Header{
				"Traceparent": []string{"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, sc trace.SpanContext) {
				if !sc.IsValid() {
					t.Error("span context should be valid")
				}
				if !sc.IsRemote {
					t.Error("span context should be marked as remote")
				}
				if !sc.Sampled {
					t.Error("span context should be sampled")
				}
				if sc.Tracestate != "" {
					t.Error("tracestate should be empty")
				}
			},
		},
		{
			name: "valid traceparent and tracestate",
			headers: http.Header{
				"Traceparent": []string{"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"},
				"Tracestate":  []string{"vendor1=value1,vendor2=value2"},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, sc trace.SpanContext) {
				if sc.Tracestate != "vendor1=value1,vendor2=value2" {
					t.Errorf("tracestate = %v, want vendor1=value1,vendor2=value2", sc.Tracestate)
				}
			},
		},
		{
			name: "multiple tracestate headers (RFC7230)",
			headers: http.Header{
				"Traceparent": []string{"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"},
				"Tracestate":  []string{"vendor1=value1", "vendor2=value2"},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, sc trace.SpanContext) {
				if sc.Tracestate != "vendor1=value1,vendor2=value2" {
					t.Errorf("tracestate = %v, want vendor1=value1,vendor2=value2", sc.Tracestate)
				}
			},
		},
		{
			name: "case-insensitive header names",
			headers: func() http.Header {
				h := http.Header{}
				h.Set("TRACEPARENT", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
				h.Set("TRACESTATE", "vendor1=value1")
				return h
			}(),
			wantErr: false,
			checkFunc: func(t *testing.T, sc trace.SpanContext) {
				if !sc.IsValid() {
					t.Error("should handle uppercase header names")
				}
			},
		},
		{
			name:    "missing traceparent",
			headers: http.Header{},
			wantErr: true,
		},
		{
			name: "invalid traceparent ignores tracestate",
			headers: http.Header{
				"Traceparent": []string{"invalid"},
				"Tracestate":  []string{"vendor1=value1"},
			},
			wantErr: true,
		},
		{
			name: "invalid tracestate ignored",
			headers: http.Header{
				"Traceparent": []string{"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"},
				"Tracestate":  []string{"invalid entry"},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, sc trace.SpanContext) {
				if sc.Tracestate != "" {
					t.Error("invalid tracestate should be ignored")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc, err := prop.Extract(tt.headers)
			if (err != nil) != tt.wantErr {
				t.Errorf("Extract() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && tt.checkFunc != nil {
				tt.checkFunc(t, sc)
			}
		})
	}
}

func TestPropagatorExtractInvalidCarrier(t *testing.T) {
	prop := &Propagator{}

	_, err := prop.Extract("not a header")
	if err == nil {
		t.Error("Extract() should return error for invalid carrier type")
	}
}

func TestPropagatorInject(t *testing.T) {
	prop := &Propagator{}

	// Create a test tracer
	tracer := trace.NewTracer(trace.TracerConfig{
		ServiceName: "test",
		Sampler:     trace.AlwaysSampler{},
	})

	// Start a span
	ctx, span := tracer.Start(context.Background(), "test")
	defer span.End()

	// Inject into headers
	headers := http.Header{}
	err := prop.Inject(ctx, headers)
	if err != nil {
		t.Fatalf("Inject() error = %v", err)
	}

	// Verify traceparent was injected
	traceparent := headers.Get("traceparent")
	if traceparent == "" {
		t.Fatal("traceparent header not injected")
	}

	// Parse and verify
	traceID, spanID, flags, err := w3c.ParseTraceparent(traceparent)
	if err != nil {
		t.Fatalf("failed to parse injected traceparent: %v", err)
	}

	if traceID != span.TraceID() {
		t.Errorf("injected trace ID mismatch: got %s, want %s", traceID.String(), span.TraceID().String())
	}

	if spanID != span.SpanID() {
		t.Errorf("injected span ID mismatch: got %s, want %s", spanID.String(), span.SpanID().String())
	}

	if (flags & w3c.SampledFlag) == 0 {
		t.Error("sampled flag should be set")
	}
}

func TestPropagatorInjectWithTracestate(t *testing.T) {
	prop := &Propagator{}

	// Create a tracer
	tracer := trace.NewTracer(trace.TracerConfig{
		ServiceName: "test",
		Sampler:     trace.AlwaysSampler{},
	})

	// Create a remote span context with tracestate
	remoteCtx := trace.NewRemoteSpanContext(
		internal.NewTraceID(),
		internal.NewSpanID(),
		"vendor1=value1,vendor2=value2",
		true,
	)

	// Start a span with remote parent
	ctx, span := tracer.Start(context.Background(), "test", trace.WithRemoteParent(remoteCtx))
	defer span.End()

	// Inject into headers
	headers := http.Header{}
	err := prop.Inject(ctx, headers)
	if err != nil {
		t.Fatalf("Inject() error = %v", err)
	}

	// Verify tracestate was propagated
	tracestate := headers.Get("tracestate")
	if tracestate != "vendor1=value1,vendor2=value2" {
		t.Errorf("tracestate = %v, want vendor1=value1,vendor2=value2", tracestate)
	}
}

func TestPropagatorInjectInvalidCarrier(t *testing.T) {
	prop := &Propagator{}

	err := prop.Inject(context.Background(), "not a header")
	if err == nil {
		t.Error("Inject() should return error for invalid carrier type")
	}
}

func TestPropagatorInjectNoSpan(t *testing.T) {
	prop := &Propagator{}

	headers := http.Header{}
	err := prop.Inject(context.Background(), headers)
	if err != nil {
		t.Errorf("Inject() should not error when no span in context, got: %v", err)
	}

	// Verify nothing was injected
	if headers.Get("traceparent") != "" {
		t.Error("traceparent should not be injected when no span in context")
	}
}

func TestPropagatorRoundTrip(t *testing.T) {
	prop := &Propagator{}

	// Create a tracer
	tracer := trace.NewTracer(trace.TracerConfig{
		ServiceName: "test",
		Sampler:     trace.AlwaysSampler{},
	})

	// Start a span
	ctx, span := tracer.Start(context.Background(), "test")
	defer span.End()

	// Inject into headers
	headers := http.Header{}
	err := prop.Inject(ctx, headers)
	if err != nil {
		t.Fatalf("Inject() error = %v", err)
	}

	// Extract from headers
	remoteCtx, err := prop.Extract(headers)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}

	// Verify trace context was preserved
	if remoteCtx.TraceID != span.TraceID() {
		t.Errorf("trace ID mismatch: got %s, want %s", remoteCtx.TraceID.String(), span.TraceID().String())
	}

	if remoteCtx.SpanID != span.SpanID() {
		t.Errorf("span ID mismatch: got %s, want %s", remoteCtx.SpanID.String(), span.SpanID().String())
	}

	if !remoteCtx.IsRemote {
		t.Error("extracted context should be marked as remote")
	}

	if !remoteCtx.Sampled {
		t.Error("extracted context should be sampled")
	}
}
