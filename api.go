package bedrock

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/metric"
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
func (c *CounterWithStatic) With(labels ...attr.Attr) *metric.CounterVec {
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
func (g *GaugeWithStatic) With(labels ...attr.Attr) *metric.GaugeVec {
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
func (h *HistogramWithStatic) With(labels ...attr.Attr) *metric.HistogramVec {
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

	cleanup := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.config.ShutdownTimeout)
		defer cancel()
		b.Shutdown(shutdownCtx)
	}

	return ctx, cleanup
}

// Operation starts a new operation and returns the operation handle and updated context.
// Success is the default state. Register errors via attr.Error() to mark as failure.
//
// Usage:
//
//	op, ctx := bedrock.Operation(ctx, "process_user")
//	defer op.Done()
//
//	op.Register(ctx, attr.String("user_id", "123"))
func Operation(ctx context.Context, name string, opts ...OperationOption) (*Op, context.Context) {
	b := bedrockFromContext(ctx)
	cfg := applyOperationOptions(name, opts)

	// Check for parent operation
	parent := operationStateFromContext(ctx)

	// Enumerate if this is a child operation with duplicate name
	fullName := name
	if parent != nil {
		parent.mu.Lock()
		count := parent.childOpCount[name]
		parent.childOpCount[name] = count + 1
		if count > 0 {
			fullName = fmt.Sprintf("%s[%d]", name, count)
		}
		parent.mu.Unlock()
		cfg.name = fullName
	}

	// Check for source config and merge attributes/labels if present
	if source := sourceConfigFromContext(ctx); source != nil {
		// Merge source attributes
		sourceAttrs := make([]attr.Attr, 0)
		source.attrs.Range(func(a attr.Attr) bool {
			sourceAttrs = append(sourceAttrs, a)
			return true
		})
		cfg.attrs = append(sourceAttrs, cfg.attrs...)

		// Use source metric labels if operation doesn't define any
		if len(cfg.metricLabels) == 0 {
			cfg.metricLabels = source.metricLabels
		}

		// Prefix operation name with source name
		cfg.name = source.name + "." + fullName
	}

	// Start trace span
	var parentCtx context.Context
	if parent != nil && parent.span != nil {
		parentCtx = trace.ContextWithSpan(ctx, parent.span)
	} else {
		parentCtx = ctx
	}

	newCtx, span := b.tracer.Start(parentCtx, cfg.name, trace.WithAttrs(cfg.attrs...))

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
//	source.Aggregate(ctx, attr.Sum("loops", 1))
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
// Usage:
//
//	step := bedrock.Step(ctx, "helper")
//	defer step.Done()
func Step(ctx context.Context, name string, attrs ...attr.Attr) *OpStep {
	return StepFromContext(ctx, name, attrs...)
}

// Register adds attributes or events to the operation.
// Attributes can be used for metrics if they match registered metric label names.
// Events are recorded in traces.
// Use attr.Error(err) to register errors and mark the operation as failed.
//
// Usage:
//
//	op.Register(ctx,
//	    attr.String("user_id", "123"),
//	    attr.NewEvent("cache.hit", attr.String("key", "user:123")),
//	    attr.Error(err),  // marks as failure if err != nil
//	)
func (op *Op) Register(ctx context.Context, items ...interface{}) {
	attrs := make([]attr.Attr, 0)

	for _, item := range items {
		switch v := item.(type) {
		case attr.Attr:
			attrs = append(attrs, v)
		case attr.Event:
			// Register as trace event
			if op.state.span != nil {
				op.state.span.AddEvent(v.Name, v.Attrs...)
			}
		}
	}

	if len(attrs) > 0 {
		op.state.setAttr(attrs...)
	}
}

// Done completes the operation and records all automatic metrics.
func (op *Op) Done() {
	if op.state == nil {
		return
	}
	op.state.end()
}

// Aggregate records aggregated metrics for the source.
// Sources typically track aggregates since they don't "complete" like operations.
// Accepts Sum, Gauge, and Histogram aggregations.
//
// Usage:
//
//	source.Aggregate(ctx,
//	    attr.Sum("requests", 1),
//	    attr.Gauge("queue_depth", 42),
//	    attr.Histogram("latency_ms", 123.45),
//	)
func (src *Src) Aggregate(ctx context.Context, items ...attr.Aggregation) {
	if src.bedrock.isNoop {
		return
	}

	for _, item := range items {
		switch v := item.(type) {
		case attr.SumAttr:
			// Record as counter
			counter := Counter(
				ctx,
				src.name+"_"+v.Key,
				"Aggregated "+v.Key+" for "+src.name,
			)
			counter.Add(v.Value)
		case attr.GaugeAttr:
			// Record as gauge
			gauge := Gauge(
				ctx,
				src.name+"_"+v.Key,
				"Aggregated "+v.Key+" for "+src.name,
			)
			gauge.Set(v.Value)
		case attr.HistogramAttr:
			// Record as histogram
			histogram := Histogram(
				ctx,
				src.name+"_"+v.Key,
				"Aggregated "+v.Key+" for "+src.name,
				nil, // use default buckets
			)
			histogram.Observe(v.Value)
		}
	}
}

// Done is a no-op for sources (they don't complete).
func (src *Src) Done() {
	// Sources don't complete, this is just for API consistency
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

func applyInitOptions(opts []InitOption) initConfig {
	cfg := initConfig{
		config:      nil,
		staticAttrs: make([]attr.Attr, 0),
	}
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

	// Include static label names
	staticLabelNames := make([]string, 0, b.staticAttr.Len())
	b.staticAttr.Range(func(a attr.Attr) bool {
		staticLabelNames = append(staticLabelNames, a.Key)
		return true
	})

	// Get static label values
	staticLabels := make([]attr.Attr, 0, b.staticAttr.Len())
	b.staticAttr.Range(func(a attr.Attr) bool {
		staticLabels = append(staticLabels, a)
		return true
	})

	allLabelNames := append(staticLabelNames, labelNames...)
	counter := b.metrics.Counter(name, help, allLabelNames...)

	return &CounterWithStatic{
		counter:      counter,
		staticLabels: staticLabels,
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

	// Include static label names
	staticLabelNames := make([]string, 0, b.staticAttr.Len())
	b.staticAttr.Range(func(a attr.Attr) bool {
		staticLabelNames = append(staticLabelNames, a.Key)
		return true
	})

	// Get static label values
	staticLabels := make([]attr.Attr, 0, b.staticAttr.Len())
	b.staticAttr.Range(func(a attr.Attr) bool {
		staticLabels = append(staticLabels, a)
		return true
	})

	allLabelNames := append(staticLabelNames, labelNames...)
	gauge := b.metrics.Gauge(name, help, allLabelNames...)

	return &GaugeWithStatic{
		gauge:        gauge,
		staticLabels: staticLabels,
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

	// Include static label names
	staticLabelNames := make([]string, 0, b.staticAttr.Len())
	b.staticAttr.Range(func(a attr.Attr) bool {
		staticLabelNames = append(staticLabelNames, a.Key)
		return true
	})

	// Get static label values
	staticLabels := make([]attr.Attr, 0, b.staticAttr.Len())
	b.staticAttr.Range(func(a attr.Attr) bool {
		staticLabels = append(staticLabels, a)
		return true
	})

	allLabelNames := append(staticLabelNames, labelNames...)
	histogram := b.metrics.Histogram(name, help, buckets, allLabelNames...)

	return &HistogramWithStatic{
		histogram:    histogram,
		staticLabels: staticLabels,
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
	b.logBridge.Log(ctx, level, msg, attrs...)
}
