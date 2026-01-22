//go:build ignore
// +build ignore

package grpc

import (
	"context"

	"github.com/kzs0/bedrock"
	"github.com/kzs0/bedrock/attr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// UnaryServerInterceptor returns a gRPC unary server interceptor that extracts
// trace context from incoming requests and starts a bedrock operation.
//
// The interceptor:
//   - Extracts W3C Trace Context from gRPC metadata
//   - Starts a bedrock operation with the remote parent
//   - Automatically marks operations as failed if the RPC returns an error
//
// Usage:
//
//	server := grpc.NewServer(
//	    grpc.UnaryInterceptor(grpc.UnaryServerInterceptor()),
//	)
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	prop := &Propagator{}

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		// Extract trace context from incoming metadata
		var opOpts []bedrock.OperationOption

		if md, ok := metadata.FromIncomingContext(ctx); ok {
			remoteCtx, err := prop.Extract(md)
			if err == nil && remoteCtx.IsValid() {
				opOpts = append(opOpts, bedrock.WithRemoteParent(remoteCtx))
			}
		}

		// Start operation for this RPC
		op, opCtx := bedrock.Operation(ctx, info.FullMethod, opOpts...)
		defer op.Done()

		// Call handler
		resp, err := handler(opCtx, req)

		// Register error if RPC failed
		if err != nil {
			op.Register(opCtx, attr.Error(err))
		}

		return resp, err
	}
}

// UnaryClientInterceptor returns a gRPC unary client interceptor that injects
// trace context into outgoing requests.
//
// The interceptor:
//   - Injects W3C Trace Context into gRPC metadata
//   - Continues the trace from the current span in the context
//
// Usage:
//
//	conn, err := grpc.Dial(
//	    target,
//	    grpc.WithUnaryInterceptor(grpc.UnaryClientInterceptor()),
//	)
func UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	prop := &Propagator{}

	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		// Get or create metadata
		md, ok := metadata.FromOutgoingContext(ctx)
		if !ok {
			md = metadata.New(nil)
		} else {
			// Copy to avoid modifying shared metadata
			md = md.Copy()
		}

		// Inject trace context into metadata
		prop.Inject(ctx, md)

		// Update context with modified metadata
		ctx = metadata.NewOutgoingContext(ctx, md)

		// Call RPC
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// StreamServerInterceptor returns a gRPC stream server interceptor that extracts
// trace context from incoming requests and starts a bedrock operation.
//
// The interceptor:
//   - Extracts W3C Trace Context from gRPC metadata
//   - Starts a bedrock operation with the remote parent
//   - Automatically marks operations as failed if the stream returns an error
//
// Usage:
//
//	server := grpc.NewServer(
//	    grpc.StreamInterceptor(grpc.StreamServerInterceptor()),
//	)
func StreamServerInterceptor() grpc.StreamServerInterceptor {
	prop := &Propagator{}

	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := ss.Context()

		// Extract trace context from incoming metadata
		var opOpts []bedrock.OperationOption

		if md, ok := metadata.FromIncomingContext(ctx); ok {
			remoteCtx, err := prop.Extract(md)
			if err == nil && remoteCtx.IsValid() {
				opOpts = append(opOpts, bedrock.WithRemoteParent(remoteCtx))
			}
		}

		// Start operation for this RPC
		op, opCtx := bedrock.Operation(ctx, info.FullMethod, opOpts...)
		defer op.Done()

		// Wrap server stream to use operation context
		wrappedStream := &wrappedServerStream{
			ServerStream: ss,
			ctx:          opCtx,
		}

		// Call handler
		err := handler(srv, wrappedStream)

		// Register error if stream failed
		if err != nil {
			op.Register(opCtx, attr.Error(err))
		}

		return err
	}
}

// StreamClientInterceptor returns a gRPC stream client interceptor that injects
// trace context into outgoing requests.
//
// The interceptor:
//   - Injects W3C Trace Context into gRPC metadata
//   - Continues the trace from the current span in the context
//
// Usage:
//
//	conn, err := grpc.Dial(
//	    target,
//	    grpc.WithStreamInterceptor(grpc.StreamClientInterceptor()),
//	)
func StreamClientInterceptor() grpc.StreamClientInterceptor {
	prop := &Propagator{}

	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		// Get or create metadata
		md, ok := metadata.FromOutgoingContext(ctx)
		if !ok {
			md = metadata.New(nil)
		} else {
			// Copy to avoid modifying shared metadata
			md = md.Copy()
		}

		// Inject trace context into metadata
		prop.Inject(ctx, md)

		// Update context with modified metadata
		ctx = metadata.NewOutgoingContext(ctx, md)

		// Call streamer
		return streamer(ctx, desc, cc, method, opts...)
	}
}

// wrappedServerStream wraps grpc.ServerStream to override Context().
type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

// Context returns the wrapper's context instead of the underlying stream's context.
func (w *wrappedServerStream) Context() context.Context {
	return w.ctx
}
