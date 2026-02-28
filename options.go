package bedrock

import (
	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace"
)

// OperationOption configures an operation.
type OperationOption interface {
	applyToOperation(*operationConfig)
}

// StepOption configures a step.
type StepOption interface {
	applyToStep(*stepConfig)
}

// commonOption is an option that works on both operations and steps.
// It implements both OperationOption and StepOption interfaces.
type commonOption struct {
	applyAttrs   []attr.Attr
	applyNoTrace bool
}

func (o commonOption) applyToOperation(c *operationConfig) {
	if len(o.applyAttrs) > 0 {
		c.attrs = append(c.attrs, o.applyAttrs...)
	}
	if o.applyNoTrace {
		c.noTrace = true
	}
}

func (o commonOption) applyToStep(c *stepConfig) {
	if len(o.applyAttrs) > 0 {
		c.attrs = append(c.attrs, o.applyAttrs...)
	}
	if o.applyNoTrace {
		c.noTrace = true
	}
}

// Attrs adds attributes to an operation or step.
// For operations, these can be used to populate metric labels if the label was registered.
func Attrs(attrs ...attr.Attr) commonOption {
	return commonOption{applyAttrs: attrs}
}

// NoTrace disables tracing for this operation/step and all children.
// Use this for hot code paths where trace telemetry would cause too much noise.
// Metrics will still be recorded for operations.
func NoTrace() commonOption {
	return commonOption{applyNoTrace: true}
}

// opOptionKind tags which field of operationOnlyOption is populated.
type opOptionKind uint8

const (
	opOptMetricLabels opOptionKind = iota + 1
	opOptSuccess
	opOptFailure
	opOptRemoteParent
)

// operationOnlyOption is an option that only works on operations.
// It uses a discriminated struct instead of a closure to avoid heap allocation.
type operationOnlyOption struct {
	kind         opOptionKind
	labelNames   []string
	err          error
	remoteParent trace.SpanContext
}

func (o operationOnlyOption) applyToOperation(c *operationConfig) {
	switch o.kind {
	case opOptMetricLabels:
		c.metricLabels = append(c.metricLabels, o.labelNames...)
	case opOptSuccess:
		c.success = true
	case opOptFailure:
		c.success = false
		c.failure = o.err
	case opOptRemoteParent:
		c.remoteParent = &o.remoteParent
	}
}

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

// MetricLabels defines the label names for this operation's metrics upfront.
// If a label is defined but no attribute with that key is set, the value will be "_".
// This prevents unlimited cardinality by pre-defining all possible label dimensions.
func MetricLabels(labelNames ...string) operationOnlyOption {
	return operationOnlyOption{kind: opOptMetricLabels, labelNames: labelNames}
}

// Success marks the operation as successful (affects auto-generated success/failure metrics).
func Success() operationOnlyOption {
	return operationOnlyOption{kind: opOptSuccess}
}

// Failure marks the operation as failed with an error.
func Failure(err error) operationOnlyOption {
	return operationOnlyOption{kind: opOptFailure, err: err}
}

// WithRemoteParent sets the remote parent from W3C Trace Context headers.
func WithRemoteParent(parent trace.SpanContext) operationOnlyOption {
	return operationOnlyOption{kind: opOptRemoteParent, remoteParent: parent}
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
func applyOperationOptions(name string, opts []OperationOption) operationConfig {
	cfg := operationConfig{name: name}
	for _, opt := range opts {
		opt.applyToOperation(&cfg)
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
	cfg := sourceConfig{name: name}
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

// applyStepOptions applies options to create a step config.
func applyStepOptions(opts []StepOption) stepConfig {
	var cfg stepConfig
	for _, opt := range opts {
		opt.applyToStep(&cfg)
	}
	return cfg
}
