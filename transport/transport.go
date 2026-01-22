// Package transport provides HTTP transport instrumentation for bedrock.
// This package contains the Transport type for advanced use cases where you need
// direct control over the http.RoundTripper. For typical usage, use the HTTP client
// functions in the root bedrock package instead.
package transport

import (
	"context"
	"fmt"
	"net/http"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace"
	httpProp "github.com/kzs0/bedrock/trace/http"
)

// Tracer is the interface for starting traces. This avoids an import cycle with the bedrock package.
type Tracer interface {
	Start(ctx context.Context, name string, opts ...trace.StartSpanOption) (context.Context, *trace.Span)
}

// Transport is an http.RoundTripper that instruments HTTP requests with bedrock.
// It automatically:
// - Injects W3C Trace Context headers (traceparent, tracestate)
// - Starts a client span for each request
// - Records metrics for request duration and status
//
// For typical usage, use bedrock.NewClient() or bedrock.Get/Post/Do() instead.
// This type is exposed for advanced cases where you need direct RoundTripper control.
type Transport struct {
	// Base is the underlying http.RoundTripper.
	// If nil, http.DefaultTransport is used.
	Base http.RoundTripper

	// Tracer is used to create spans. If nil, tracing is disabled.
	// This is typically set by bedrock.NewClient() or provided via context.
	Tracer Tracer
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	// Check if we have a tracer
	if t.Tracer == nil {
		// No tracer, just pass through
		return t.base().RoundTrip(req)
	}

	// Start a client span for this request
	spanName := fmt.Sprintf("HTTP %s", req.Method)

	spanCtx, span := t.Tracer.Start(ctx, spanName,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttrs(
			attr.String("http.method", req.Method),
			attr.String("http.url", req.URL.String()),
			attr.String("http.host", req.URL.Host),
			attr.String("http.scheme", req.URL.Scheme),
			attr.String("http.target", req.URL.Path),
		),
	)
	defer span.End()

	// Inject W3C Trace Context headers
	prop := &httpProp.Propagator{}
	prop.Inject(spanCtx, req.Header)

	// Update request context to include span
	req = req.WithContext(spanCtx)

	// Execute request
	resp, err := t.base().RoundTrip(req)

	// Record response attributes
	if err != nil {
		span.RecordError(err)
		span.SetStatus(trace.StatusError, err.Error())
		return resp, err
	}

	if resp != nil {
		span.SetAttr(attr.Int("http.status_code", resp.StatusCode))

		// Mark as error if status code is 4xx or 5xx
		if resp.StatusCode >= 400 {
			span.SetStatus(trace.StatusError, fmt.Sprintf("HTTP %d", resp.StatusCode))
		} else {
			span.SetStatus(trace.StatusOK, "")
		}
	}

	return resp, nil
}

// base returns the base RoundTripper, defaulting to http.DefaultTransport.
func (t *Transport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}
