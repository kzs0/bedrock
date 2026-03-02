package otlp

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/internal/h2c"
	"github.com/kzs0/bedrock/trace"
)

// otlpGRPCPath is the gRPC method path for OTLP trace export.
const otlpGRPCPath = "/opentelemetry.proto.collector.trace.v1.TraceService/Export"

// GRPCExporterConfig configures the OTLP gRPC exporter.
type GRPCExporterConfig struct {
	// Endpoint is the gRPC collector address: "host:port" (e.g. "localhost:4317").
	// Do NOT include a scheme — use Insecure to toggle TLS vs plaintext.
	Endpoint string

	// Insecure disables TLS. Set to true for local collectors (h2c prior knowledge).
	// Default: false (TLS via ALPN h2).
	Insecure bool

	// Headers are extra metadata sent on every RPC.
	Headers map[string]string

	// Timeout is the per-export deadline. Default: 10 s.
	Timeout time.Duration

	// ServiceName is the OTLP resource service.name attribute.
	ServiceName string

	// Resource holds additional resource-level attributes.
	Resource attr.Set
}

// GRPCExporter exports spans to an OTLP collector via gRPC over HTTP/2.
// It uses only the Go standard library: cleartext h2c (via internal/h2c)
// for non-TLS endpoints, and net/http's built-in HTTP/2 for TLS endpoints.
type GRPCExporter struct {
	cfg    GRPCExporterConfig
	client *h2c.Client

	mu      sync.Mutex
	stopped bool
}

// NewGRPCExporter creates a new OTLP gRPC exporter.
func NewGRPCExporter(cfg GRPCExporterConfig) *GRPCExporter {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &GRPCExporter{
		cfg:    cfg,
		client: h2c.NewClient(cfg.Endpoint, !cfg.Insecure, cfg.Timeout),
	}
}

// ExportSpans implements trace.Exporter.
func (e *GRPCExporter) ExportSpans(ctx context.Context, spans []*trace.Span) error {
	e.mu.Lock()
	stopped := e.stopped
	e.mu.Unlock()
	if stopped || len(spans) == 0 {
		return nil
	}

	// Encode spans to OTLP protobuf
	protoBytes, err := ProtoEncodeSpans(spans, e.cfg.ServiceName, e.cfg.Resource)
	if err != nil {
		return fmt.Errorf("otlp/grpc: encode spans: %w", err)
	}

	// Apply gRPC length-prefix framing:
	//   [1 byte: compressed-flag (0)] [4 bytes: big-endian message length] [message]
	grpcFrame := make([]byte, 5+len(protoBytes))
	grpcFrame[0] = 0 // not compressed
	binary.BigEndian.PutUint32(grpcFrame[1:], uint32(len(protoBytes)))
	copy(grpcFrame[5:], protoBytes)

	_, err = e.client.Do(otlpGRPCPath, grpcFrame, e.cfg.Headers)
	if err != nil {
		return fmt.Errorf("otlp/grpc: export: %w", err)
	}
	return nil
}

// Shutdown stops the exporter and releases the connection.
func (e *GRPCExporter) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	e.stopped = true
	e.mu.Unlock()
	e.client.Shutdown()
	return nil
}
