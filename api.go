package bedrock

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/metric"
	"github.com/kzs0/bedrock/server"
	"github.com/kzs0/bedrock/trace"
)

// Op is a handle to an operation.
type Op struct {
	state *operationState
}

// Src is a handle to a source.
type Src struct {
	bedrock *Bedrock
	name    string
	config  *sourceConfig
}

// CounterWithStatic wraps a metric.Counter and automatically includes static labels.
type CounterWithStatic struct {
	counter      *metric.Counter
	staticLabels []attr.Attr
}

// With returns a CounterVec with the given label values plus static labels.
func (c *CounterWithStatic) With(labels ...attr.Attr) metric.CounterVec {
	allLabels := append(c.staticLabels, labels...)
	return c.counter.With(allLabels...)
}

// Inc increments the counter by 1 with static labels.
func (c *CounterWithStatic) Inc() {
	c.counter.With(c.staticLabels...).Inc()
}

// Add adds the given value to the counter with static labels.
func (c *CounterWithStatic) Add(v float64) {
	c.counter.With(c.staticLabels...).Add(v)
}

// GaugeWithStatic wraps a metric.Gauge and automatically includes static labels.
type GaugeWithStatic struct {
	gauge        *metric.Gauge
	staticLabels []attr.Attr
}

// With returns a GaugeVec with the given label values plus static labels.
func (g *GaugeWithStatic) With(labels ...attr.Attr) metric.GaugeVec {
	allLabels := append(g.staticLabels, labels...)
	return g.gauge.With(allLabels...)
}

// Set sets the gauge to the given value with static labels.
func (g *GaugeWithStatic) Set(v float64) {
	g.gauge.With(g.staticLabels...).Set(v)
}

// Inc increments the gauge by 1 with static labels.
func (g *GaugeWithStatic) Inc() {
	g.gauge.With(g.staticLabels...).Inc()
}

// Dec decrements the gauge by 1 with static labels.
func (g *GaugeWithStatic) Dec() {
	g.gauge.With(g.staticLabels...).Dec()
}

// Add adds the given value to the gauge with static labels.
func (g *GaugeWithStatic) Add(v float64) {
	g.gauge.With(g.staticLabels...).Add(v)
}

// Sub subtracts the given value from the gauge with static labels.
func (g *GaugeWithStatic) Sub(v float64) {
	g.gauge.With(g.staticLabels...).Sub(v)
}

// HistogramWithStatic wraps a metric.Histogram and automatically includes static labels.
type HistogramWithStatic struct {
	histogram    *metric.Histogram
	staticLabels []attr.Attr
}

// With returns a HistogramVec with the given label values plus static labels.
func (h *HistogramWithStatic) With(labels ...attr.Attr) metric.HistogramVec {
	allLabels := append(h.staticLabels, labels...)
	return h.histogram.With(allLabels...)
}

// Observe records an observation with static labels.
func (h *HistogramWithStatic) Observe(v float64) {
	h.histogram.With(h.staticLabels...).Observe(v)
}

// Init initializes bedrock in the context and returns a context with bedrock attached
// and a cleanup function. If no config is provided, it loads from environment variables.
//
// The observability server is automatically started if Config.ServerEnabled is true.
// Set ServerEnabled to true in your config to enable automatic server startup.
//
// Usage:
//
//	ctx, close := bedrock.Init(ctx, bedrock.WithConfig(cfg))
//	defer close()
func Init(ctx context.Context, opts ...InitOption) (context.Context, func()) {
	cfg := applyInitOptions(opts)

	// If no config provided, load from environment
	if cfg.config == nil {
		envCfg, err := FromEnv()
		if err != nil {
			// Fall back to defaults
			envCfg = DefaultConfig()
		}
		cfg.config = &envCfg
	}

	b, err := New(*cfg.config, cfg.staticAttrs...)
	if err != nil {
		panic(fmt.Errorf("bedrock: failed to initialize: %w", err))
	}

	ctx = WithBedrock(ctx, b)

	// Automatically create and start obs server if enabled in config
	var obsServer *server.Server
	if cfg.config.ServerEnabled {
		serverCfg := cfg.config.serverConfig()
		obsServer = server.New(b.metrics, serverCfg)
		go func() {
			if err := obsServer.ListenAndServe(); err != nil {
				// Only log if it's not a graceful shutdown
				if err.Error() != "http: Server closed" {
					b.logger.Error("observability server error", slog.Any("error", err))
				}
			}
		}()
	}

	cleanup := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.config.ShutdownTimeout)
		defer cancel()

		// Shutdown obs server first if it exists
		if obsServer != nil {
			if err := obsServer.Shutdown(shutdownCtx); err != nil {
				b.logger.Error("failed to shutdown observability server", slog.Any("error", err))
			}
		}

		if err := b.Shutdown(shutdownCtx); err != nil {
			b.logger.Error("failed to shutdown bedrock", slog.Any("error", err))
		}
	}

	return ctx, cleanup
}

// Operation starts a new operation and returns the operation handle and updated context.
// Success is the default state. Register errors via attr.Error() to mark as failure.
//
// Accepts both common options (Attrs, NoTrace) and operation-specific options (MetricLabels, etc).
//
// Usage:
//
//	op, ctx := bedrock.Operation(ctx, "process_user")
//	defer op.Done()
//
//	op, ctx := bedrock.Operation(ctx, "hot_path", bedrock.NoTrace())
//	op.Register(ctx, attr.String("user_id", "123"))
func Operation(ctx context.Context, name string, opts ...OperationOption) (*Op, context.Context) {
	b := bedrockFromContext(ctx)
	if b.isNoop {
		return globalNoopOp, ctx
	}
	cfg := applyOperationOptions(name, opts)

	// Check for parent operation
	parent := operationStateFromContext(ctx)

	// Check for source config and merge attributes/labels if present
	if source := sourceConfigFromContext(ctx); source != nil {
		// Prepend source attrs directly from the existing slice — no copy needed
		// when cfg.attrs is nil (append returns source slice as-is).
		cfg.attrs = append(source.attrs.Attrs(), cfg.attrs...)

		// Use source metric labels if operation doesn't define any
		if len(cfg.metricLabels) == 0 {
			cfg.metricLabels = source.metricLabels
		}

		// Prefix operation name with source name
		cfg.name = source.name + "." + name
	}

	// Inherit no-trace mode from context or check if explicitly set
	noTrace := cfg.noTrace || isNoTrace(ctx)

	var span *trace.Span
	var newCtx context.Context

	if noTrace {
		// Skip tracing, just pass through context with no-trace flag
		newCtx = withNoTrace(ctx)
	} else {
		// Resolve local parent span (passed explicitly to avoid a context read).
		var parentSpan *trace.Span
		if parent != nil {
			parentSpan = parent.span
		}
		// StartSpan avoids closure allocations: parent and remote parent are
		// direct parameters; attrs are synced to span at Done() via SetAttrSet.
		newCtx, span = b.tracer.StartSpan(ctx, cfg.name, parentSpan, cfg.remoteParent, cfg.attrs)
	}

	// Create operation state
	state := newOperationState(b, span, cfg.name, cfg, parent)

	// Store operation state in context
	newCtx = withOperationState(newCtx, state)

	// Return operation handle
	return &Op{state: state}, newCtx
}

// Source registers a source in the context and returns the source handle.
// Sources are for long-running processes that spawn operations.
//
// Usage:
//
//	source, ctx := bedrock.Source(ctx, "background.worker")
//	defer source.Done()
//
//	source.Sum(ctx, "loops", 1)
func Source(ctx context.Context, name string, opts ...SourceOption) (*Src, context.Context) {
	cfg := applySourceOptions(name, opts)
	ctx = withSourceConfig(ctx, &cfg)

	b := bedrockFromContext(ctx)

	return &Src{
		bedrock: b,
		name:    name,
		config:  &cfg,
	}, ctx
}

// Step creates a lightweight step within an operation for tracing without full operation metrics.
// Steps are part of their parent operation and contribute attributes/events to it.
// Use this for helper functions where you want trace visibility but not separate metrics.
//
// Accepts common options (Attrs, NoTrace).
//
// Usage:
//
//	step := bedrock.Step(ctx, "helper")
//	defer step.Done()
//
//	step := bedrock.Step(ctx, "helper", bedrock.Attrs(attr.String("key", "value")))
//	step := bedrock.Step(ctx, "hot_path", bedrock.NoTrace())
func Step(ctx context.Context, name string, opts ...StepOption) *OpStep {
	return StepFromContext(ctx, name, opts...)
}

// Register adds attributes to the operation.
// Attributes can be used for metrics if they match registered metric label names.
// Use attr.Error(err) to register errors and mark the operation as failed.
//
// Usage:
//
//	op.Register(ctx,
//	    attr.String("user_id", "123"),
//	    attr.Error(err),  // marks as failure if err != nil
//	)
func (op *Op) Register(ctx context.Context, attrs ...attr.Attr) {
	if op.state == nil || len(attrs) == 0 {
		return
	}
	op.state.setAttr(attrs...)
}

// Event records a trace event on the operation span.
//
// Usage:
//
//	op.Event(ctx, attr.NewEvent("cache.hit", attr.String("key", "user:123")))
func (op *Op) Event(ctx context.Context, event attr.Event) {
	if op.state == nil || op.state.span == nil {
		return
	}
	op.state.span.AddEvent(event.Name, event.Attrs...)
}

// Done completes the operation and records all automatic metrics.
func (op *Op) Done() {
	if op.state == nil {
		return
	}
	op.state.end()
}

// Sum increments a named counter for the source by the given value.
//
// Usage:
//
//	source.Sum(ctx, "jobs_processed", 1)
func (src *Src) Sum(ctx context.Context, key string, value float64) {
	if src.bedrock.isNoop {
		return
	}
	counter := Counter(ctx, src.name+"_"+key, "Aggregated "+key+" for "+src.name)
	counter.Add(value)
}

// Gauge sets a named gauge for the source to the given value.
//
// Usage:
//
//	source.Gauge(ctx, "queue_depth", 42)
func (src *Src) Gauge(ctx context.Context, key string, value float64) {
	if src.bedrock.isNoop {
		return
	}
	gauge := Gauge(ctx, src.name+"_"+key, "Aggregated "+key+" for "+src.name)
	gauge.Set(value)
}

// Histogram records a named histogram observation for the source.
//
// Usage:
//
//	source.Histogram(ctx, "latency_ms", 123.45)
func (src *Src) Histogram(ctx context.Context, key string, value float64) {
	if src.bedrock.isNoop {
		return
	}
	histogram := Histogram(ctx, src.name+"_"+key, "Aggregated "+key+" for "+src.name, nil)
	histogram.Observe(value)
}

// Done signals the source is stopping.
// When LogCanonical is enabled it emits a structured completion log.
func (src *Src) Done() {
	if src.bedrock.isNoop || !src.bedrock.config.LogCanonical {
		return
	}
	src.bedrock.logger.Info("source.complete",
		"source", src.name,
		"success", true,
	)
}

// InitOption configures initialization.
type InitOption func(*initConfig)

type initConfig struct {
	config      *Config
	staticAttrs []attr.Attr
}

// WithConfig provides an explicit configuration.
func WithConfig(cfg Config) InitOption {
	return func(c *initConfig) {
		c.config = &cfg
	}
}

// WithStaticAttrs sets static attributes for all operations.
func WithStaticAttrs(attrs ...attr.Attr) InitOption {
	return func(c *initConfig) {
		c.staticAttrs = append(c.staticAttrs, attrs...)
	}
}

// WithLogLevel sets the log level for the bedrock instance.
// Valid levels: "debug", "info", "warn", "error"
// This is a convenience wrapper that modifies the config.
//
// Usage:
//
//	ctx, close := bedrock.Init(ctx, bedrock.WithLogLevel("debug"))
func WithLogLevel(level string) InitOption {
	return func(c *initConfig) {
		if c.config == nil {
			c.config = &Config{}
		}
		c.config.LogLevel = level
	}
}

func applyInitOptions(opts []InitOption) initConfig {
	var cfg initConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// Counter creates or retrieves a counter metric from the bedrock instance in context.
// Static labels are automatically included when recording values.
//
// Usage:
//
//	counter := bedrock.Counter(ctx, "http_requests_total", "Total HTTP requests", "method", "status")
//	counter.With(attr.String("method", "GET"), attr.String("status", "200")).Inc()
//	// Or without additional labels:
//	counter.Inc() // automatically includes static labels
func Counter(ctx context.Context, name, help string, labelNames ...string) *CounterWithStatic {
	b := bedrockFromContext(ctx)
	allLabelNames := append(b.staticLabelKeys, labelNames...)
	return &CounterWithStatic{
		counter:      b.metrics.Counter(name, help, allLabelNames...),
		staticLabels: b.staticLabelVals,
	}
}

// Gauge creates or retrieves a gauge metric from the bedrock instance in context.
// Static labels are automatically included when recording values.
//
// Usage:
//
//	gauge := bedrock.Gauge(ctx, "active_connections", "Active connections")
//	gauge.Set(42) // automatically includes static labels
func Gauge(ctx context.Context, name, help string, labelNames ...string) *GaugeWithStatic {
	b := bedrockFromContext(ctx)
	allLabelNames := append(b.staticLabelKeys, labelNames...)
	return &GaugeWithStatic{
		gauge:        b.metrics.Gauge(name, help, allLabelNames...),
		staticLabels: b.staticLabelVals,
	}
}

// Histogram creates or retrieves a histogram metric from the bedrock instance in context.
// Uses default buckets if buckets is nil.
// Static labels are automatically included when recording values.
//
// Usage:
//
//	hist := bedrock.Histogram(ctx, "request_duration_ms", "Request duration", nil, "method")
//	hist.With(attr.String("method", "GET")).Observe(123.45)
//	// Or without additional labels:
//	hist.Observe(123.45) // automatically includes static labels
func Histogram(ctx context.Context, name, help string, buckets []float64, labelNames ...string) *HistogramWithStatic {
	b := bedrockFromContext(ctx)
	allLabelNames := append(b.staticLabelKeys, labelNames...)
	return &HistogramWithStatic{
		histogram:    b.metrics.Histogram(name, help, buckets, allLabelNames...),
		staticLabels: b.staticLabelVals,
	}
}

// Debug logs a debug message with the given attributes.
// Uses the bedrock logger from context, which includes static attributes.
//
// Usage:
//
//	bedrock.Debug(ctx, "processing request", attr.String("user_id", "123"))
func Debug(ctx context.Context, msg string, attrs ...attr.Attr) {
	b := bedrockFromContext(ctx)
	if b.isNoop {
		return
	}
	b.logBridge.Debug(ctx, msg, attrs...)
}

// Info logs an info message with the given attributes.
// Uses the bedrock logger from context, which includes static attributes.
//
// Usage:
//
//	bedrock.Info(ctx, "request completed", attr.Int("status", 200))
func Info(ctx context.Context, msg string, attrs ...attr.Attr) {
	b := bedrockFromContext(ctx)
	if b.isNoop {
		return
	}
	b.logBridge.Info(ctx, msg, attrs...)
}

// Warn logs a warning message with the given attributes.
// Uses the bedrock logger from context, which includes static attributes.
//
// Usage:
//
//	bedrock.Warn(ctx, "high latency detected", attr.Duration("latency", 5*time.Second))
func Warn(ctx context.Context, msg string, attrs ...attr.Attr) {
	b := bedrockFromContext(ctx)
	if b.isNoop {
		return
	}
	b.logBridge.Warn(ctx, msg, attrs...)
}

// Error logs an error message with the given attributes.
// Uses the bedrock logger from context, which includes static attributes.
//
// Usage:
//
//	bedrock.Error(ctx, "database connection failed", attr.Error(err))
func Error(ctx context.Context, msg string, attrs ...attr.Attr) {
	b := bedrockFromContext(ctx)
	if b.isNoop {
		return
	}
	b.logBridge.Error(ctx, msg, attrs...)
}

// Log logs a message at the given level with attributes.
// Uses the bedrock logger from context, which includes static attributes.
//
// Usage:
//
//	bedrock.Log(ctx, slog.LevelInfo, "custom log", attr.String("key", "value"))
func Log(ctx context.Context, level slog.Level, msg string, attrs ...attr.Attr) {
	b := bedrockFromContext(ctx)
	if b.isNoop {
		return
	}
	b.logBridge.Log(ctx, level, msg, attrs...)
}
