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
	TraceID internal.TraceID
	SpanID  internal.SpanID
}

// IsValid returns true if the span context has valid IDs.
func (sc SpanContext) IsValid() bool {
	return !sc.TraceID.IsZero() && !sc.SpanID.IsZero()
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
		TraceID: span.traceID,
		SpanID:  span.spanID,
	}
}
