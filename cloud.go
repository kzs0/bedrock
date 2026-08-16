package bedrock

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/kzs0/bedrock/trace"
	"github.com/kzs0/bedrock/trace/otlp"
)

const (
	defaultCloudEndpoint       = "https://ingest.bedrock.dev"
	defaultCloudPushInterval   = 15 * time.Second
	defaultProfileInterval     = 60 * time.Second
	defaultProfileCPUSampleDur = 10 * time.Second
)

// cloudConfig holds configuration for the Bedrock managed cloud backend.
type cloudConfig struct {
	apiKey              string
	endpoint            string
	insecure            bool
	pushInterval        time.Duration
	profileInterval     time.Duration
	profileCPUSampleDur time.Duration
}

// CloudOption configures the cloud backend connection.
type CloudOption func(*cloudConfig)

// CloudEndpoint overrides the default ingest URL (https://ingest.bedrock.dev).
func CloudEndpoint(url string) CloudOption {
	return func(c *cloudConfig) { c.endpoint = url }
}

// CloudInsecure disables TLS for the gRPC trace connection. Intended for local
// development against a local OTLP collector.
func CloudInsecure() CloudOption {
	return func(c *cloudConfig) { c.insecure = true }
}

// CloudPushInterval sets the interval at which Prometheus metrics are pushed to
// the managed platform (default 15s).
func CloudPushInterval(d time.Duration) CloudOption {
	return func(c *cloudConfig) { c.pushInterval = d }
}

// CloudProfileInterval sets the interval between continuous profiling snapshots
// (default 60s).
func CloudProfileInterval(d time.Duration) CloudOption {
	return func(c *cloudConfig) { c.profileInterval = d }
}

// CloudProfileCPUSampleDuration sets how long the CPU profiler collects samples
// within each profiling interval (default 10s).
func CloudProfileCPUSampleDuration(d time.Duration) CloudOption {
	return func(c *cloudConfig) { c.profileCPUSampleDur = d }
}

// WithCloud configures the SDK to export telemetry to the Bedrock managed
// platform. The apiKey is sent as an x-bedrock-key gRPC metadata header for
// traces and as an Authorization: Bearer header for metrics and profiles.
//
// When WithCloud is combined with a local BEDROCK_TRACE_URL, spans are exported
// to both destinations via a fan-out exporter.
//
// Usage:
//
//	ctx, close := bedrock.Init(context.Background(),
//	    bedrock.WithCloud("brk_live_abc123"),
//	)
//	defer close()
func WithCloud(apiKey string, opts ...CloudOption) InitOption {
	return func(c *initConfig) {
		cc := &cloudConfig{
			apiKey:              apiKey,
			endpoint:            defaultCloudEndpoint,
			pushInterval:        defaultCloudPushInterval,
			profileInterval:     defaultProfileInterval,
			profileCPUSampleDur: defaultProfileCPUSampleDur,
		}
		for _, o := range opts {
			o(cc)
		}
		if cc.pushInterval <= 0 {
			cc.pushInterval = defaultCloudPushInterval
		}
		if cc.profileInterval <= 0 {
			cc.profileInterval = defaultProfileInterval
		}
		c.cloudCfg = cc
	}
}

// wireCloud sets up the cloud trace exporter, metrics push, and profiling push
// after the Bedrock instance has been created by New(). It returns a shutdown
// function that flushes all pending cloud exports.
func wireCloud(b *Bedrock, cc *cloudConfig, logger *slog.Logger) func(context.Context) {
	// Build the gRPC endpoint (strip http/https scheme; the h2c client expects host:port).
	grpcEndpoint := cloudGRPCEndpoint(cc.endpoint)

	cloudGRPC := otlp.NewGRPCExporter(otlp.GRPCExporterConfig{
		Endpoint:    grpcEndpoint,
		Insecure:    cc.insecure || !strings.HasPrefix(cc.endpoint, "https"),
		Headers:     map[string]string{"x-bedrock-key": cc.apiKey},
		ServiceName: b.config.Service,
		Resource:    b.staticAttr,
	})
	return wireCloudWithTraceExporter(b, cc, logger, cloudGRPC)
}

// wireCloudWithTraceExporter wires an already constructed cloud trace
// exporter. Bedrock owns exporter shutdown so its batch processor can flush
// before the exporter is closed.
func wireCloudWithTraceExporter(
	b *Bedrock,
	cc *cloudConfig,
	logger *slog.Logger,
	cloudTrace trace.Exporter,
) func(context.Context) {
	retryExp := &retryExporter{base: cloudTrace, maxRetries: 3, logger: logger}

	// Fan-out: keep local exporter if one exists, add cloud.
	var topExp trace.Exporter
	if b.rawExporter != nil {
		topExp = &fanOutExporter{exporters: []trace.Exporter{b.rawExporter, retryExp}}
	} else {
		topExp = retryExp
	}

	// Replace the existing batch processor with a larger-buffered cloud one.
	// The old batch processor (if any) is abandoned here — its goroutine will
	// drain naturally since we're swapping before any spans are enqueued from
	// user code (wiring happens inside Init before the context is returned).
	cloudBatchCfg := otlp.BatchProcessorConfig{
		MaxQueueSize: 10000,
		BatchSize:    512,
		BatchTimeout: 5 * time.Second,
	}
	newBatch := otlp.NewBatchProcessor(topExp, cloudBatchCfg)
	b.batchProcessor = newBatch
	b.rawExporter = topExp
	b.tracer.SetExporter(newBatch)

	// Start background workers.
	stopMetrics := startMetricsPush(b.metrics, logger, *cc)
	stopProfiles := startProfilePush(b.config.Service, b.staticAttr, logger, *cc)

	return func(ctx context.Context) {
		stopCloudWorkers(ctx, stopMetrics, stopProfiles)
	}
}

func stopCloudWorkers(
	ctx context.Context,
	stopMetrics func(context.Context),
	stopProfiles func(context.Context),
) {
	// Signal profile cancellation before the potentially slow final metrics
	// push. A canceled context makes this first call non-blocking while still
	// triggering the profile worker's one-shot cancellation.
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	stopProfiles(cancelCtx)
	stopMetrics(ctx)
	stopProfiles(ctx)
}

// cloudGRPCEndpoint converts a URL like "https://ingest.bedrock.dev" to a bare
// "host:port" string expected by the gRPC exporter.
func cloudGRPCEndpoint(endpoint string) string {
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")
	// Strip trailing path if any
	if idx := strings.IndexByte(endpoint, '/'); idx >= 0 {
		endpoint = endpoint[:idx]
	}
	// Add default OTLP gRPC port if no port specified
	if !strings.ContainsRune(endpoint, ':') {
		endpoint += ":4317"
	}
	return endpoint
}

// ── retryExporter ─────────────────────────────────────────────────────────────

// retryExporter wraps a trace.Exporter and retries failed ExportSpans calls
// with exponential back-off (initialDelay, 2×, 4×, …). It logs a warning on
// each retry and an error when all retries are exhausted.
type retryExporter struct {
	base         trace.Exporter
	maxRetries   int
	initialDelay time.Duration // defaults to 1s when zero
	logger       *slog.Logger
}

func (r *retryExporter) ExportSpans(ctx context.Context, spans []*trace.Span) error {
	var err error
	delay := r.initialDelay
	if delay == 0 {
		delay = time.Second
	}
	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		err = r.base.ExportSpans(ctx, spans)
		if err == nil {
			return nil
		}
		if attempt < r.maxRetries {
			if r.logger != nil {
				r.logger.Warn("cloud trace export failed, retrying",
					slog.Int("attempt", attempt+1),
					slog.Duration("backoff", delay),
					slog.String("error", err.Error()),
				)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			delay *= 2
		}
	}
	if r.logger != nil {
		r.logger.Error("cloud trace export failed after retries", slog.String("error", err.Error()))
	}
	return err
}

func (r *retryExporter) Shutdown(ctx context.Context) error {
	return r.base.Shutdown(ctx)
}

// ── fanOutExporter ────────────────────────────────────────────────────────────

// fanOutExporter sends spans to multiple trace.Exporter instances. It does not
// implement SpanEnqueuer intentionally — a shared BatchProcessor wraps it so
// all destinations share the same queue and batching strategy.
type fanOutExporter struct {
	exporters []trace.Exporter
}

func (f *fanOutExporter) ExportSpans(ctx context.Context, spans []*trace.Span) error {
	var firstErr error
	for _, exp := range f.exporters {
		if err := exp.ExportSpans(ctx, spans); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (f *fanOutExporter) Shutdown(ctx context.Context) error {
	var firstErr error
	for _, exp := range f.exporters {
		if err := exp.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
