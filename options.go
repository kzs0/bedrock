package bedrock

import (
	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace"
)

// commonConfig is the interface for common configuration options
// shared between operations and steps.
type commonConfig interface {
	addAttrs(attrs ...attr.Attr)
	setNoTrace(v bool)
}

// Option is a common option that can be applied to both operations and steps.
type Option func(commonConfig)

// Attrs adds attributes to an operation or step.
// For operations, these can be used to populate metric labels if the label was registered.
func Attrs(attrs ...attr.Attr) Option {
	return func(cfg commonConfig) {
		cfg.addAttrs(attrs...)
	}
}

// NoTrace disables tracing for this operation/step and all children.
// Use this for hot code paths where trace telemetry would cause too much noise.
// Metrics will still be recorded for operations.
func NoTrace() Option {
	return func(cfg commonConfig) {
		cfg.setNoTrace(true)
	}
}

// OperationOption configures an operation.
type OperationOption func(*operationConfig)

// operationConfig holds configuration for an operation.
type operationConfig struct {
	name         string
	attrs        []attr.Attr
	metricLabels []string           // defined metric label names (registered upfront)
	success      bool               // whether the operation succeeded (for auto metrics)
	failure      error              // error if operation failed
	remoteParent *trace.SpanContext // remote parent from W3C Trace Context
	noTrace      bool               // if true, skip tracing for this operation and children
}

// Implement commonConfig interface for operationConfig
func (c *operationConfig) addAttrs(attrs ...attr.Attr) {
	c.attrs = append(c.attrs, attrs...)
}

func (c *operationConfig) setNoTrace(v bool) {
	c.noTrace = v
}

// MetricLabels defines the label names for this operation's metrics upfront.
// If a label is defined but no attribute with that key is set, the value will be "_".
// This prevents unlimited cardinality by pre-defining all possible label dimensions.
func MetricLabels(labelNames ...string) OperationOption {
	return func(cfg *operationConfig) {
		cfg.metricLabels = append(cfg.metricLabels, labelNames...)
	}
}

// Success marks the operation as successful (affects auto-generated success/failure metrics).
func Success() OperationOption {
	return func(cfg *operationConfig) {
		cfg.success = true
	}
}

// Failure marks the operation as failed with an error.
func Failure(err error) OperationOption {
	return func(cfg *operationConfig) {
		cfg.success = false
		cfg.failure = err
	}
}

// WithRemoteParent sets the remote parent from W3C Trace Context headers.
func WithRemoteParent(parent trace.SpanContext) OperationOption {
	return func(cfg *operationConfig) {
		cfg.remoteParent = &parent
	}
}

// EndOption configures how an operation ends.
type EndOption func(*endConfig)

// endConfig holds configuration for ending an operation.
type endConfig struct {
	success bool
	failure error
	hasOpts bool // whether any options were provided
}

// EndSuccess marks the operation as successful when ending.
func EndSuccess() EndOption {
	return func(cfg *endConfig) {
		cfg.success = true
		cfg.hasOpts = true
	}
}

// EndFailure marks the operation as failed when ending.
func EndFailure(err error) EndOption {
	return func(cfg *endConfig) {
		cfg.success = false
		cfg.failure = err
		cfg.hasOpts = true
	}
}

// applyOperationOptions applies options to create an operation config.
func applyOperationOptions(name string, commonOpts []Option, opOpts []OperationOption) operationConfig {
	cfg := operationConfig{
		name:         name,
		attrs:        make([]attr.Attr, 0),
		metricLabels: make([]string, 0),
		success:      false,
	}
	for _, opt := range commonOpts {
		opt(&cfg)
	}
	for _, opt := range opOpts {
		opt(&cfg)
	}
	return cfg
}

// SourceOption configures a source.
type SourceOption func(*sourceConfig)

// sourceConfig holds configuration for a source.
type sourceConfig struct {
	name         string
	attrs        attr.Set
	metricLabels []string // defined metric label names for operations from this source
}

// SourceAttrs adds attributes to a source.
func SourceAttrs(attrs ...attr.Attr) SourceOption {
	return func(cfg *sourceConfig) {
		cfg.attrs = cfg.attrs.Merge(attrs...)
	}
}

// SourceMetricLabels defines the label names for operations started from this source.
// All operations from this source will use these as their metric label names.
// If an operation doesn't provide a value for a label, it will be set to "_".
func SourceMetricLabels(labelNames ...string) SourceOption {
	return func(cfg *sourceConfig) {
		cfg.metricLabels = append(cfg.metricLabels, labelNames...)
	}
}

// applySourceOptions applies options to create a source config.
func applySourceOptions(name string, opts []SourceOption) sourceConfig {
	cfg := sourceConfig{
		name:         name,
		attrs:        attr.NewSet(),
		metricLabels: make([]string, 0),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// stepConfig holds configuration for a step.
type stepConfig struct {
	attrs   []attr.Attr
	noTrace bool // if true, skip tracing for this step
}

// Implement commonConfig interface for stepConfig
func (c *stepConfig) addAttrs(attrs ...attr.Attr) {
	c.attrs = append(c.attrs, attrs...)
}

func (c *stepConfig) setNoTrace(v bool) {
	c.noTrace = v
}

// applyStepOptions applies options to create a step config.
func applyStepOptions(opts []Option) stepConfig {
	cfg := stepConfig{
		attrs: make([]attr.Attr, 0),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}
