package bedrock

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"

	"github.com/kzs0/bedrock/attr"
	blog "github.com/kzs0/bedrock/log"
	"github.com/kzs0/bedrock/metric"
	"github.com/kzs0/bedrock/trace"
	"github.com/kzs0/bedrock/trace/otlp"
)

// cachedOpMetrics holds the pre-looked-up metric objects for a given operation name.
// This avoids string concatenation and registry map lookups on every Done() call.
type cachedOpMetrics struct {
	count      *metric.Counter
	success    *metric.Counter
	failure    *metric.Counter
	duration   *metric.Histogram
	labelNames []string // static label keys + operation-specific metric label names
}

// Bedrock is the main entry point for observability.
type Bedrock struct {
	config     Config
	logger     *slog.Logger
	logBridge  *blog.Bridge
	tracer     *trace.Tracer
	metrics    *metric.Registry
	staticAttr attr.Set

	// Cached slices of static label keys and values, computed once at init.
	staticLabelKeys []string
	staticLabelVals []attr.Attr

	// Per-operation-name metric cache: avoids string allocs and map lookups on Done().
	opMetricsCache sync.Map // map[string]*cachedOpMetrics

	// rawExporter is the underlying span exporter (HTTP or gRPC). It may be nil.
	// batchProcessor wraps rawExporter and is what the tracer actually calls.
	rawExporter      trace.Exporter
	batchProcessor   *otlp.BatchProcessor
	runtimeCollector *metric.RuntimeCollector

	isNoop bool // true if this is a noop instance

	shutdownOnce     sync.Once
	shutdownFinalize sync.Once
	shutdownDone     chan struct{}
	shutdownErr      error
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
	// Bedrock and its registry own their metric bucket configuration. This keeps
	// later caller mutations from changing live metric behavior.
	cfg.MetricBuckets = append([]float64(nil), cfg.MetricBuckets...)

	b := &Bedrock{
		config:     cfg,
		staticAttr: attr.NewSet(staticAttrs...),
		metrics:    metric.NewRegistryWithBuckets(cfg.MetricPrefix, cfg.MetricBuckets),
	}

	// Pre-build cached slices of static label keys and values (never change after init).
	n := b.staticAttr.Len()
	if n > 0 {
		b.staticLabelKeys = make([]string, 0, n)
		b.staticLabelVals = make([]attr.Attr, 0, n)
		b.staticAttr.Range(func(a attr.Attr) bool {
			b.staticLabelKeys = append(b.staticLabelKeys, a.Key)
			b.staticLabelVals = append(b.staticLabelVals, a)
			return true
		})
	}

	// Setup logging
	handler := blog.NewHandler(&blog.HandlerOptions{
		Level:     cfg.logLevel(),
		Output:    cfg.LogOutput,
		Format:    cfg.LogFormat,
		AddSource: cfg.LogAddSource,
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
		if useGRPC(cfg) {
			b.rawExporter = otlp.NewGRPCExporter(otlp.GRPCExporterConfig{
				Endpoint:    cfg.TraceURL,
				Insecure:    cfg.TraceInsecure,
				ServiceName: cfg.Service,
				Resource:    b.staticAttr,
				Timeout:     cfg.ShutdownTimeout,
			})
		} else {
			b.rawExporter = otlp.NewExporter(otlp.ExporterConfig{
				Endpoint:    cfg.TraceURL,
				ServiceName: cfg.Service,
				Resource:    b.staticAttr,
			})
		}
		b.batchProcessor = otlp.NewBatchProcessor(b.rawExporter, otlp.DefaultBatchConfig())
		// Wire the batch processor as the tracer exporter so that span.End()
		// enqueues into the batch processor rather than spawning a goroutine per span.
		exporter = b.batchProcessor
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

	// Setup runtime metrics collector if enabled
	if cfg.RuntimeMetrics {
		// Get static labels for runtime metrics
		staticLabels := make([]attr.Attr, 0, b.staticAttr.Len())
		b.staticAttr.Range(func(a attr.Attr) bool {
			staticLabels = append(staticLabels, a)
			return true
		})

		b.runtimeCollector = metric.NewRuntimeCollector(b.metrics, staticLabels...)
		b.metrics.RegisterCollector(b.runtimeCollector)
	}

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

// MetricsRegistry returns the metric registry (alias for Metrics).
func (b *Bedrock) MetricsRegistry() *metric.Registry {
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

// getOpMetrics returns the cached metric objects for the given operation name,
// creating them on first use. This avoids string concatenation and registry
// map lookups on the hot Done() path.
func (b *Bedrock) getOpMetrics(name string, metricLabels []string) *cachedOpMetrics {
	if v, ok := b.opMetricsCache.Load(name); ok {
		return v.(*cachedOpMetrics)
	}

	// Build combined label names: static + operation-specific.
	allLabels := make([]string, len(b.staticLabelKeys)+len(metricLabels))
	copy(allLabels, b.staticLabelKeys)
	copy(allLabels[len(b.staticLabelKeys):], metricLabels)

	cm := &cachedOpMetrics{
		count:      b.metrics.Counter(name+"_count", "Total count of "+name+" operations", allLabels...),
		success:    b.metrics.Counter(name+"_successes", "Successful "+name+" operations", allLabels...),
		failure:    b.metrics.Counter(name+"_failures", "Failed "+name+" operations", allLabels...),
		duration:   b.metrics.Histogram(name+"_duration_ms", "Duration of "+name+" operations in milliseconds", nil, allLabels...),
		labelNames: allLabels,
	}

	// Store with LoadOrStore to handle concurrent first calls.
	if actual, loaded := b.opMetricsCache.LoadOrStore(name, cm); loaded {
		return actual.(*cachedOpMetrics)
	}
	return cm
}

// Shutdown gracefully shuts down all components.
func (b *Bedrock) Shutdown(ctx context.Context) error {
	b.shutdownOnce.Do(func() {
		b.shutdownDone = make(chan struct{})
	})

	select {
	case <-b.shutdownDone:
		return b.shutdownErr
	default:
	}

	var drainErr error
	if b.batchProcessor != nil {
		drainErr = b.batchProcessor.Shutdown(ctx)
		if ctx.Err() != nil {
			// The processor owns the in-progress drain. A later caller with a
			// live context can resume waiting and safely finalize the exporter.
			return errors.Join(drainErr, ctx.Err())
		}
	}

	b.shutdownFinalize.Do(func() {
		go func() {
			var exporterErr error
			switch {
			case b.rawExporter != nil:
				exporterErr = b.rawExporter.Shutdown(ctx)
			case b.batchProcessor == nil && b.tracer != nil:
				exporterErr = b.tracer.Shutdown(ctx)
			}
			b.shutdownErr = errors.Join(drainErr, exporterErr)
			close(b.shutdownDone)
		}()
	})

	select {
	case <-b.shutdownDone:
		return b.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
