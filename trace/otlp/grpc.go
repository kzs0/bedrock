package otlp

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
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
// It uses only the Go standard library and the internal HTTP/2 transport for
// both cleartext h2c and TLS with ALPN.
type GRPCExporter struct {
	cfg    GRPCExporterConfig
	client *h2c.Client
	cfgErr error

	mu           sync.Mutex
	stopped      bool
	exports      sync.WaitGroup
	shutdownOnce sync.Once
	shutdownDone chan struct{}
}

// NewGRPCExporter creates a new OTLP gRPC exporter.
func NewGRPCExporter(cfg GRPCExporterConfig) *GRPCExporter {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	headers, cfgErr := copyGRPCHeaders(cfg.Headers)
	cfg.Headers = headers
	return &GRPCExporter{
		cfg: cfg, client: h2c.NewClient(cfg.Endpoint, !cfg.Insecure, cfg.Timeout),
		cfgErr: cfgErr, shutdownDone: make(chan struct{}),
	}
}

// ExportSpans implements trace.Exporter.
func (e *GRPCExporter) ExportSpans(ctx context.Context, spans []*trace.Span) error {
	e.mu.Lock()
	if e.stopped || len(spans) == 0 {
		e.mu.Unlock()
		return nil
	}
	e.exports.Add(1)
	e.mu.Unlock()
	defer e.exports.Done()

	if e.cfgErr != nil {
		return e.cfgErr
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
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

	_, err = e.client.DoContext(ctx, otlpGRPCPath, grpcFrame, e.cfg.Headers)
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
	e.shutdownOnce.Do(func() {
		go func() {
			e.exports.Wait()
			e.client.Shutdown()
			close(e.shutdownDone)
		}()
	})
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-e.shutdownDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func copyGRPCHeaders(headers map[string]string) (map[string]string, error) {
	if headers == nil {
		return nil, nil
	}
	copy := make(map[string]string, len(headers))
	for name, value := range headers {
		lower := strings.ToLower(name)
		if !validGRPCMetadataName(lower) || !validGRPCMetadataValue(value) {
			return nil, fmt.Errorf("otlp/grpc: invalid metadata %q", name)
		}
		if _, exists := copy[lower]; exists {
			return nil, fmt.Errorf("otlp/grpc: duplicate metadata %q", lower)
		}
		copy[lower] = value
	}
	return copy, nil
}

func validGRPCMetadataValue(value string) bool {
	for _, ch := range value {
		if ch < 0x20 || ch > 0x7e {
			return false
		}
	}
	return true
}

func validGRPCMetadataName(name string) bool {
	if name == "" || strings.HasPrefix(name, ":") {
		return false
	}
	switch name {
	case "connection", "keep-alive", "proxy-connection", "transfer-encoding", "upgrade", "content-type", "te":
		return false
	}
	for _, ch := range name {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' && ch != '_' && ch != '.' {
			return false
		}
	}
	return true
}
