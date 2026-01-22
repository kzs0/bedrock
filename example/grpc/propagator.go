//go:build ignore
// +build ignore

// Package grpc provides an example implementation of W3C Trace Context propagation for gRPC.
//
// This is a reference implementation showing how to implement the trace.Propagator
// interface for gRPC metadata. Copy this code into your own project and adapt as needed.
//
// This package requires the google.golang.org/grpc dependency.
// This file uses build tag 'ignore' to prevent it from being built by default.
package grpc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kzs0/bedrock/trace"
	"github.com/kzs0/bedrock/trace/w3c"
	"google.golang.org/grpc/metadata"
)

const (
	traceparentKey = "traceparent"
	tracestateKey  = "tracestate"
)

// Propagator implements trace.Propagator for gRPC metadata using W3C Trace Context format.
// It extracts and injects traceparent and tracestate in gRPC metadata.
//
// The carrier must be a metadata.MD.
//
// Usage:
//
//	prop := &grpc.Propagator{}
//
//	// Extract from incoming RPC (server-side)
//	md, ok := metadata.FromIncomingContext(ctx)
//	if ok {
//	    remoteCtx, err := prop.Extract(md)
//	    if err == nil && remoteCtx.IsValid() {
//	        op, ctx := bedrock.Operation(ctx, "handler", bedrock.WithRemoteParent(remoteCtx))
//	        defer op.Done()
//	    }
//	}
//
//	// Inject into outgoing RPC (client-side)
//	md := metadata.New(nil)
//	prop.Inject(ctx, md)
//	ctx = metadata.NewOutgoingContext(ctx, md)
type Propagator struct{}

// Extract extracts W3C Trace Context from gRPC metadata.
// Returns a remote SpanContext with trace ID, span ID, tracestate, and sampled flag.
//
// Per gRPC metadata conventions:
//   - Metadata keys are case-insensitive
//   - Values are stored as string slices (first value is used)
//   - Uses same W3C format as HTTP (traceparent/tracestate)
//
// The carrier must be a metadata.MD, otherwise an error is returned.
func (p *Propagator) Extract(carrier any) (trace.SpanContext, error) {
	md, ok := carrier.(metadata.MD)
	if !ok {
		return trace.SpanContext{}, errors.New("carrier must be metadata.MD")
	}

	// Extract traceparent (gRPC metadata is case-insensitive, stored lowercase)
	traceparentValues := md.Get(traceparentKey)
	if len(traceparentValues) == 0 {
		return trace.SpanContext{}, errors.New("traceparent not found in metadata")
	}
	traceparent := traceparentValues[0]

	// Parse traceparent using W3C utilities
	traceID, parentID, flags, err := w3c.ParseTraceparent(traceparent)
	if err != nil {
		// Invalid traceparent: ignore both headers and start new trace
		return trace.SpanContext{}, fmt.Errorf("failed to parse traceparent: %w", err)
	}

	// Extract sampled flag (bit 0)
	sampled := (flags & w3c.SampledFlag) != 0

	// Extract tracestate
	var tracestate string
	tracestateValues := md.Get(tracestateKey)
	if len(tracestateValues) > 0 {
		// gRPC metadata can have multiple values; combine with comma per W3C spec
		tracestate = strings.Join(tracestateValues, ",")

		// Validate tracestate (but don't fail if invalid, just ignore it)
		_, err := w3c.ParseTracestate(tracestate)
		if err != nil {
			// Invalid tracestate: continue with empty tracestate
			tracestate = ""
		}
	}

	return trace.NewRemoteSpanContext(traceID, parentID, tracestate, sampled), nil
}

// Inject injects W3C Trace Context into gRPC metadata.
// Sets traceparent and tracestate in metadata from the current span context.
//
// Per gRPC metadata conventions:
//   - Metadata keys should be lowercase
//   - Values are stored as string slices
//   - Uses same W3C format as HTTP (traceparent/tracestate)
//
// The carrier must be a metadata.MD, otherwise an error is returned.
//
// If no span is present in ctx or the span is not recording, this is a no-op.
func (p *Propagator) Inject(ctx context.Context, carrier any) error {
	md, ok := carrier.(metadata.MD)
	if !ok {
		return errors.New("carrier must be metadata.MD")
	}

	span := trace.SpanFromContext(ctx)
	if span == nil || !span.IsRecording() {
		return nil
	}

	// Get span's sampled status
	// For now, assume recording = sampled
	sampled := true

	// Format and set traceparent using W3C utilities
	traceparent := w3c.FormatTraceparent(span.TraceID(), span.SpanID(), sampled)
	md.Set(traceparentKey, traceparent)

	// Propagate tracestate if present in the span
	spanCtx := trace.SpanContextFromContext(ctx)
	if spanCtx.Tracestate != "" {
		md.Set(tracestateKey, spanCtx.Tracestate)
	}

	return nil
}
