//go:build ignore
// +build ignore

// Package grpc provides an example implementation of W3C Trace Context propagation for gRPC.
//
// This is a reference implementation showing how to implement distributed tracing
// for gRPC services using Bedrock. Copy this code into your own project and adapt as needed.
//
// This package requires the google.golang.org/grpc dependency.
//
// This package implements the trace.Propagator interface for gRPC metadata and provides
// convenient interceptors for both client and server-side tracing.
//
// # Server-Side Instrumentation
//
// Use the provided interceptors when creating your gRPC server:
//
//	import (
//	    "github.com/kzs0/bedrock"
//	    bedrockGrpc "github.com/kzs0/bedrock/trace/grpc"
//	    "google.golang.org/grpc"
//	)
//
//	func main() {
//	    ctx, close := bedrock.Init(context.Background())
//	    defer close()
//
//	    server := grpc.NewServer(
//	        grpc.UnaryInterceptor(bedrockGrpc.UnaryServerInterceptor()),
//	        grpc.StreamInterceptor(bedrockGrpc.StreamServerInterceptor()),
//	    )
//
//	    // Register your services...
//	    pb.RegisterYourServiceServer(server, &yourService{})
//
//	    listener, _ := net.Listen("tcp", ":50051")
//	    server.Serve(listener)
//	}
//
// The server interceptors automatically:
//   - Extract W3C Trace Context from incoming gRPC metadata
//   - Start a bedrock operation for each RPC
//   - Link child spans to the remote parent trace
//   - Mark operations as failed if the RPC returns an error
//
// # Client-Side Instrumentation
//
// Use the provided interceptors when creating your gRPC client connection:
//
//	conn, err := grpc.Dial(
//	    "localhost:50051",
//	    grpc.WithUnaryInterceptor(bedrockGrpc.UnaryClientInterceptor()),
//	    grpc.WithStreamInterceptor(bedrockGrpc.StreamClientInterceptor()),
//	)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer conn.Close()
//
//	client := pb.NewYourServiceClient(conn)
//
// The client interceptors automatically:
//   - Inject W3C Trace Context into outgoing gRPC metadata
//   - Continue the trace from the current span in the context
//   - Enable distributed tracing across service boundaries
//
// # Manual Propagation
//
// For advanced use cases, you can use the Propagator directly:
//
//	prop := &bedrockGrpc.Propagator{}
//
//	// Extract from incoming context (server-side)
//	if md, ok := metadata.FromIncomingContext(ctx); ok {
//	    remoteCtx, err := prop.Extract(md)
//	    if err == nil && remoteCtx.IsValid() {
//	        op, ctx := bedrock.Operation(ctx, "my-handler",
//	            bedrock.WithRemoteParent(remoteCtx))
//	        defer op.Done()
//	    }
//	}
//
//	// Inject into outgoing context (client-side)
//	md := metadata.New(nil)
//	prop.Inject(ctx, md)
//	ctx = metadata.NewOutgoingContext(ctx, md)
//
// # Distributed Tracing Flow
//
// When a client calls a server with instrumentation on both sides:
//
//	Client                                Server
//	  |                                      |
//	  | Start span (trace_id: ABC)           |
//	  | Inject traceparent in metadata       |
//	  |------------------------------------->|
//	  |                                      | Extract traceparent
//	  |                                      | Start child span (trace_id: ABC, parent: client_span)
//	  |                                      | Process request
//	  |<-------------------------------------|
//	  | End span                             | End span
//
// Both spans share the same trace_id, enabling full trace visualization in tools like Jaeger.
package grpc
