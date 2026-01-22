package trace

import (
	"context"

	"github.com/kzs0/bedrock/internal"
)

type contextKey int

const (
	spanContextKey contextKey = iota
)

// SpanContext contains the identifiers for a span.
type SpanContext struct {
	TraceID    internal.TraceID
	SpanID     internal.SpanID
	Tracestate string // W3C tracestate for passthrough propagation
	IsRemote   bool   // true if extracted from W3C traceparent header
	Sampled    bool   // sampled flag from W3C traceparent
}

// IsValid returns true if the span context has valid IDs.
func (sc SpanContext) IsValid() bool {
	return !sc.TraceID.IsZero() && !sc.SpanID.IsZero()
}

// NewRemoteSpanContext creates a SpanContext from W3C Trace Context headers.
func NewRemoteSpanContext(traceID internal.TraceID, spanID internal.SpanID, tracestate string, sampled bool) SpanContext {
	return SpanContext{
		TraceID:    traceID,
		SpanID:     spanID,
		Tracestate: tracestate,
		IsRemote:   true,
		Sampled:    sampled,
	}
}

// ContextWithSpan returns a new context with the span attached.
func ContextWithSpan(ctx context.Context, span *Span) context.Context {
	return context.WithValue(ctx, spanContextKey, span)
}

// SpanFromContext returns the span from the context, or nil if none.
func SpanFromContext(ctx context.Context) *Span {
	if span, ok := ctx.Value(spanContextKey).(*Span); ok {
		return span
	}
	return nil
}

// SpanContextFromContext returns the span context from the context.
func SpanContextFromContext(ctx context.Context) SpanContext {
	span := SpanFromContext(ctx)
	if span == nil {
		return SpanContext{}
	}
	return SpanContext{
		TraceID:    span.traceID,
		SpanID:     span.spanID,
		Tracestate: span.tracestate,
		IsRemote:   false, // Local span
		Sampled:    true,  // If span exists, it's sampled (not dropped)
	}
}
