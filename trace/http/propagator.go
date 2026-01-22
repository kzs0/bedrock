// Package http provides W3C Trace Context propagation for HTTP transports.
package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/kzs0/bedrock/trace"
	"github.com/kzs0/bedrock/trace/w3c"
)

const (
	traceparentHeader = "traceparent"
	tracestateHeader  = "tracestate"
)

// Propagator implements trace.Propagator for HTTP headers using W3C Trace Context format.
// It extracts and injects traceparent and tracestate headers per the W3C specification.
//
// The carrier must be an http.Header.
//
// Usage:
//
//	prop := &http.Propagator{}
//
//	// Extract from incoming request
//	remoteCtx, err := prop.Extract(request.Header)
//	if err == nil && remoteCtx.IsValid() {
//	    op, ctx := bedrock.Operation(ctx, "handler", bedrock.WithRemoteParent(remoteCtx))
//	    defer op.Done()
//	}
//
//	// Inject into outgoing request
//	prop.Inject(ctx, request.Header)
type Propagator struct{}

// Extract extracts W3C Trace Context from HTTP headers.
// Returns a remote SpanContext with trace ID, span ID, tracestate, and sampled flag.
//
// Per W3C spec:
//   - Header names are case-insensitive
//   - If traceparent is invalid, tracestate must be ignored
//   - If traceparent is missing, both are ignored
//   - Multiple tracestate headers are combined per RFC7230
//
// The carrier must be an http.Header, otherwise an error is returned.
func (p *Propagator) Extract(carrier any) (trace.SpanContext, error) {
	headers, ok := carrier.(http.Header)
	if !ok {
		return trace.SpanContext{}, errors.New("carrier must be http.Header")
	}

	// Extract traceparent (case-insensitive)
	traceparent := headers.Get(traceparentHeader)
	if traceparent == "" {
		return trace.SpanContext{}, errors.New("traceparent header not found")
	}

	// Parse traceparent using W3C utilities
	traceID, parentID, flags, err := w3c.ParseTraceparent(traceparent)
	if err != nil {
		// Invalid traceparent: ignore both headers and start new trace
		return trace.SpanContext{}, fmt.Errorf("failed to parse traceparent: %w", err)
	}

	// Extract sampled flag (bit 0)
	sampled := (flags & w3c.SampledFlag) != 0

	// Extract tracestate (case-insensitive)
	// Per RFC7230, multiple headers with same name should be combined
	tracestateHeaders := headers.Values(tracestateHeader)
	var tracestate string
	if len(tracestateHeaders) > 0 {
		// Combine multiple headers with comma
		tracestate = strings.Join(tracestateHeaders, ",")

		// Validate tracestate (but don't fail if invalid, just ignore it)
		_, err := w3c.ParseTracestate(tracestate)
		if err != nil {
			// Invalid tracestate: continue with empty tracestate
			tracestate = ""
		}
	}

	return trace.NewRemoteSpanContext(traceID, parentID, tracestate, sampled), nil
}

// Inject injects W3C Trace Context into HTTP headers.
// Sets traceparent and tracestate headers from the current span context.
//
// Per W3C spec:
//   - Header names should be lowercase
//   - If traceparent is modified, tracestate may be modified
//   - If traceparent is unchanged, tracestate must not be modified
//
// The carrier must be an http.Header, otherwise an error is returned.
//
// If no span is present in ctx or the span is not recording, this is a no-op.
func (p *Propagator) Inject(ctx context.Context, carrier any) error {
	headers, ok := carrier.(http.Header)
	if !ok {
		return errors.New("carrier must be http.Header")
	}

	span := trace.SpanFromContext(ctx)
	if span == nil || !span.IsRecording() {
		return nil
	}

	// Get span's sampled status
	// For now, assume recording = sampled
	sampled := true

	// Format and set traceparent header using W3C utilities
	traceparent := w3c.FormatTraceparent(span.TraceID(), span.SpanID(), sampled)
	headers.Set(traceparentHeader, traceparent)

	// Propagate tracestate if present in the span
	// The span stores tracestate from remote parent for propagation
	spanCtx := trace.SpanContextFromContext(ctx)
	if spanCtx.Tracestate != "" {
		headers.Set(tracestateHeader, spanCtx.Tracestate)
	}

	return nil
}
