package otlp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace"
)

// ExporterConfig configures the OTLP exporter.
type ExporterConfig struct {
	// Endpoint is the OTLP HTTP endpoint (e.g., "http://localhost:4318/v1/traces").
	Endpoint string
	// Headers are additional HTTP headers to send.
	Headers map[string]string
	// Timeout is the HTTP request timeout.
	Timeout time.Duration
	// ServiceName is the name of the service.
	ServiceName string
	// Resource contains additional resource attributes.
	Resource attr.Set
	// Insecure allows HTTP instead of HTTPS.
	Insecure bool
}

// Exporter exports spans to an OTLP endpoint.
type Exporter struct {
	cfg    ExporterConfig
	client *http.Client

	mu      sync.Mutex
	stopped bool
}

// NewExporter creates a new OTLP exporter.
func NewExporter(cfg ExporterConfig) *Exporter {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}

	return &Exporter{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// ExportSpans exports spans to the OTLP endpoint.
func (e *Exporter) ExportSpans(ctx context.Context, spans []*trace.Span) error {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()

	if len(spans) == 0 {
		return nil
	}

	// Encode spans
	data, err := EncodeSpans(spans, e.cfg.ServiceName, e.cfg.Resource)
	if err != nil {
		return fmt.Errorf("otlp: failed to encode spans: %w", err)
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, "POST", e.cfg.Endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("otlp: failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range e.cfg.Headers {
		req.Header.Set(k, v)
	}

	// Send request
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("otlp: failed to send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Check response
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("otlp: server returned %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// Shutdown stops the exporter.
func (e *Exporter) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	e.stopped = true
	e.mu.Unlock()
	return nil
}
