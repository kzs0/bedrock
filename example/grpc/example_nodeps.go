//go:build ignore
// +build ignore

// This file shows how to implement gRPC propagation without using this package.
// Use this as a reference if you want to avoid the grpc dependency.

package main

import (
	"context"
	"errors"

	"github.com/kzs0/bedrock/trace"
	"github.com/kzs0/bedrock/trace/w3c"
)

// Example: Custom gRPC metadata type (without importing google.golang.org/grpc/metadata)
type Metadata map[string][]string

// CustomGRPCPropagator implements trace.Propagator for gRPC without external dependencies.
type CustomGRPCPropagator struct{}

func (p *CustomGRPCPropagator) Extract(carrier any) (trace.SpanContext, error) {
	md, ok := carrier.(Metadata)
	if !ok {
		return trace.SpanContext{}, errors.New("carrier must be Metadata")
	}

	// Extract traceparent
	traceparentValues := md["traceparent"]
	if len(traceparentValues) == 0 {
		return trace.SpanContext{}, errors.New("traceparent not found")
	}

	// Parse using W3C utilities
	traceID, spanID, flags, err := w3c.ParseTraceparent(traceparentValues[0])
	if err != nil {
		return trace.SpanContext{}, err
	}

	sampled := (flags & w3c.SampledFlag) != 0

	// Extract tracestate
	var tracestate string
	if tracestateValues := md["tracestate"]; len(tracestateValues) > 0 {
		tracestate = tracestateValues[0]
	}

	return trace.NewRemoteSpanContext(traceID, spanID, tracestate, sampled), nil
}

func (p *CustomGRPCPropagator) Inject(ctx context.Context, carrier any) error {
	md, ok := carrier.(Metadata)
	if !ok {
		return errors.New("carrier must be Metadata")
	}

	span := trace.SpanFromContext(ctx)
	if span == nil || !span.IsRecording() {
		return nil
	}

	// Format using W3C utilities
	traceparent := w3c.FormatTraceparent(span.TraceID(), span.SpanID(), true)
	md["traceparent"] = []string{traceparent}

	// Propagate tracestate
	spanCtx := trace.SpanContextFromContext(ctx)
	if spanCtx.Tracestate != "" {
		md["tracestate"] = []string{spanCtx.Tracestate}
	}

	return nil
}

// Example usage:
func main() {
	// Extract from incoming metadata
	incomingMD := Metadata{
		"traceparent": []string{"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"},
	}

	prop := &CustomGRPCPropagator{}
	remoteCtx, _ := prop.Extract(incomingMD)

	// Use with bedrock operation
	_ = remoteCtx // Use with bedrock.WithRemoteParent(remoteCtx)

	// Inject into outgoing metadata
	outgoingMD := make(Metadata)
	prop.Inject(context.Background(), outgoingMD)
}
