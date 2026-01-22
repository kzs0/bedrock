# gRPC Propagation Example

This directory contains a reference implementation of W3C Trace Context propagation for gRPC services using Bedrock.

## Overview

This example shows how to:
- Implement the `trace.Propagator` interface for gRPC metadata
- Create gRPC interceptors for automatic trace propagation
- Support both unary and streaming RPCs
- Maintain trace context across service boundaries

## Usage

**Copy this code into your own project** and adapt as needed. This is not part of the core Bedrock library to avoid adding gRPC as a dependency.

### Installation

This code uses the `ignore` build tag, so it won't be built by default. To use it:

1. **Copy the files into your project:**
   ```bash
   # Remove the build tags
   cp example/grpc/propagator.go yourproject/grpc/
   cp example/grpc/interceptor.go yourproject/grpc/
   
   # Edit each file to remove the first 3 lines:
   # //go:build ignore
   # // +build ignore
   # (blank line)
   ```

2. **Add gRPC dependency:**
   ```bash
   cd yourproject
   go get google.golang.org/grpc
   ```

### Server-Side

```go
import (
    "github.com/kzs0/bedrock"
    bedrockGrpc "yourproject/grpc"  // Your copy of this example
    "google.golang.org/grpc"
)

func main() {
    ctx, close := bedrock.Init(context.Background())
    defer close()

    server := grpc.NewServer(
        grpc.UnaryInterceptor(bedrockGrpc.UnaryServerInterceptor()),
        grpc.StreamInterceptor(bedrockGrpc.StreamServerInterceptor()),
    )

    // Register your services...
    pb.RegisterYourServiceServer(server, &yourService{})

    listener, _ := net.Listen("tcp", ":50051")
    server.Serve(listener)
}
```

### Client-Side

```go
conn, err := grpc.Dial(
    "localhost:50051",
    grpc.WithUnaryInterceptor(bedrockGrpc.UnaryClientInterceptor()),
    grpc.WithStreamInterceptor(bedrockGrpc.StreamClientInterceptor()),
)
if err != nil {
    log.Fatal(err)
}
defer conn.Close()

client := pb.NewYourServiceClient(conn)
```

## Implementation Details

### Propagator

The `Propagator` implements the `trace.Propagator` interface:

```go
type Propagator interface {
    Extract(carrier any) (trace.SpanContext, error)
    Inject(ctx context.Context, carrier any) error
}
```

It expects the carrier to be `metadata.MD` (gRPC metadata).

### Interceptors

The interceptors automatically:
- Extract trace context from incoming requests (server-side)
- Start bedrock operations for each RPC
- Inject trace context into outgoing requests (client-side)
- Record errors when RPCs fail

## Customization

You can customize this example for your needs:
- Add custom attributes to operations
- Change operation naming conventions
- Add custom error handling
- Implement additional metadata propagation

## Why Not in Core Bedrock?

To keep Bedrock dependency-free, gRPC support is provided as an example rather than a built-in package. This approach:
- Avoids forcing `google.golang.org/grpc` on all users
- Allows you to customize the implementation for your needs
- Keeps the core library focused and lightweight

## Alternative: No External Dependencies

If you want to avoid the gRPC dependency entirely, see `example_nodeps.go` for a simple example showing how to implement propagation without importing `google.golang.org/grpc/metadata`.
