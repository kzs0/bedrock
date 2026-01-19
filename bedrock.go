package bedrock

import (
	"context"
	"log/slog"
	"os"

	"github.com/kzs0/bedrock/attr"
	blog "github.com/kzs0/bedrock/log"
	"github.com/kzs0/bedrock/metric"
	"github.com/kzs0/bedrock/trace"
	"github.com/kzs0/bedrock/trace/otlp"
)

// Bedrock is the main entry point for observability.
type Bedrock struct {
	config     Config
	logger     *slog.Logger
	logBridge  *blog.Bridge
	tracer     *trace.Tracer
	metrics    *metric.Registry
	staticAttr attr.Set

	exporter       *otlp.Exporter
	batchProcessor *otlp.BatchProcessor

	isNoop bool // true if this is a noop instance
}

// New creates a new Bedrock instance with the given configuration.
func New(cfg Config, staticAttrs ...attr.Attr) (*Bedrock, error) {
	// Apply defaults
	if cfg.Service == "" {
		cfg.Service = "unknown"
	}
	if cfg.ShutdownTimeout == 0 {
		cfg.ShutdownTimeout = DefaultConfig().ShutdownTimeout
	}
	if cfg.LogOutput == nil {
		cfg.LogOutput = os.Stderr
	}

	b := &Bedrock{
		config:     cfg,
		staticAttr: attr.NewSet(staticAttrs...),
		metrics:    metric.NewRegistry(),
	}

	// Setup logging
	handler := blog.NewHandler(&blog.HandlerOptions{
		Level:  cfg.logLevel(),
		Output: cfg.LogOutput,
		Format: cfg.LogFormat,
	})
	handler.SetTraceContextFunc(func(ctx context.Context) (traceID, spanID string) {
		span := trace.SpanFromContext(ctx)
		if span != nil {
			return span.TraceID().String(), span.SpanID().String()
		}
		return "", ""
	})

	// Add static attributes to logger
	slogAttrs := make([]slog.Attr, 0, b.staticAttr.Len())
	b.staticAttr.Range(func(a attr.Attr) bool {
		slogAttrs = append(slogAttrs, blog.AttrToSlog(a))
		return true
	})

	var loggerHandler slog.Handler = handler
	if len(slogAttrs) > 0 {
		loggerHandler = handler.WithAttrs(slogAttrs)
	}

	b.logger = slog.New(loggerHandler)
	b.logBridge = blog.NewBridge(b.logger)

	// Setup tracing
	var exporter trace.Exporter
	if cfg.TraceURL != "" {
		b.exporter = otlp.NewExporter(otlp.ExporterConfig{
			Endpoint:    cfg.TraceURL,
			ServiceName: cfg.Service,
			Resource:    b.staticAttr,
		})
		b.batchProcessor = otlp.NewBatchProcessor(b.exporter, otlp.DefaultBatchConfig())
		exporter = b.exporter
	}

	sampler := cfg.TraceSampler
	if sampler == nil {
		// Use sample rate from config
		if cfg.TraceSampleRate > 0 && cfg.TraceSampleRate < 1.0 {
			sampler = trace.NewRatioSampler(cfg.TraceSampleRate)
		} else {
			sampler = trace.AlwaysSampler{}
		}
	}

	b.tracer = trace.NewTracer(trace.TracerConfig{
		ServiceName: cfg.Service,
		Resource:    b.staticAttr,
		Sampler:     sampler,
		Exporter:    exporter,
	})

	return b, nil
}

// Logger returns the underlying slog.Logger.
func (b *Bedrock) Logger() *slog.Logger {
	return b.logger
}

// Metrics returns the metric registry.
func (b *Bedrock) Metrics() *metric.Registry {
	return b.metrics
}

// Tracer returns the tracer.
func (b *Bedrock) Tracer() *trace.Tracer {
	return b.tracer
}

// IsNoop returns true if this is a noop bedrock instance.
func (b *Bedrock) IsNoop() bool {
	return b.isNoop
}

// Shutdown gracefully shuts down all components.
func (b *Bedrock) Shutdown(ctx context.Context) error {
	if b.batchProcessor != nil {
		if err := b.batchProcessor.Shutdown(ctx); err != nil {
			return err
		}
	}
	if b.tracer != nil {
		if err := b.tracer.Shutdown(ctx); err != nil {
			return err
		}
	}
	return nil
}
