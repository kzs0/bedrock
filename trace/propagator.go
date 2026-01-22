package trace

import "context"

// Propagator is the interface for extracting and injecting trace context across process boundaries.
// Implementations are responsible for reading and writing trace context in a transport-specific
// carrier (e.g., http.Header, grpc metadata.MD, message queue headers).
//
// The Propagator interface follows the OpenTelemetry propagator pattern and enables
// distributed tracing across heterogeneous systems.
//
// Example implementations:
//   - HTTP: carrier is http.Header
//   - gRPC: carrier is metadata.MD
//   - Kafka: carrier is []kafka.Header
//   - AMQP: carrier is amqp.Table
type Propagator interface {
	// Extract reads trace context from the carrier and returns a remote SpanContext.
	// The carrier type is implementation-specific (e.g., http.Header for HTTP propagator).
	//
	// Returns an error if:
	//   - The carrier is not the expected type
	//   - The trace context is malformed or invalid
	//   - The trace context is missing (optional behavior - some implementations may return empty context)
	//
	// If extraction succeeds, the returned SpanContext should have IsRemote=true and
	// can be passed to Operation/Source via WithRemoteParent() option.
	Extract(carrier any) (SpanContext, error)

	// Inject writes the current trace context from ctx into the carrier.
	// The carrier type is implementation-specific (e.g., http.Header for HTTP propagator).
	//
	// The trace context is obtained from the active span in ctx via SpanFromContext().
	// If no span is present or the span is not recording, this should be a no-op.
	//
	// Returns an error if:
	//   - The carrier is not the expected type
	//   - Writing to the carrier fails
	Inject(ctx context.Context, carrier any) error
}
