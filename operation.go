package bedrock

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace"
)

// OpStep is a handle to a step within an operation.
// Steps contribute their attributes to the parent operation.
type OpStep struct {
	span   *trace.Span
	attrs  attr.Set
	parent *operationState
	ctx    context.Context
}

// operationState is the internal state of an operation.
// This is stored in the context and should not be exposed to users.
type operationState struct {
	mu sync.Mutex

	bedrock      *Bedrock
	span         *trace.Span
	name         string
	startTime    time.Time
	attrs        attr.Set
	metricLabels []string // defined label names (upfront registration)
	parent       *operationState
	success      bool
	failure      error

	// Child tracking for enumeration
	steps        []*OpStep
	stepCounts   map[string]int // count of steps by name for enumeration
	childOpCount map[string]int // count of child operations by name
}

// newOperationState creates a new operation state.
func newOperationState(b *Bedrock, span *trace.Span, name string, cfg operationConfig, parent *operationState) *operationState {
	return &operationState{
		bedrock:      b,
		span:         span,
		name:         name,
		startTime:    time.Now(),
		attrs:        attr.NewSet(cfg.attrs...),
		metricLabels: cfg.metricLabels,
		parent:       parent,
		success:      true, // Default to success
		steps:        make([]*OpStep, 0),
		stepCounts:   make(map[string]int),
		childOpCount: make(map[string]int),
	}
}

// setAttr adds or updates attributes on the operation.
func (op *operationState) setAttr(attrs ...attr.Attr) {
	op.mu.Lock()
	defer op.mu.Unlock()

	op.attrs = op.attrs.Merge(attrs...)
	if op.span != nil {
		op.span.SetAttr(attrs...)
	}

	// Check for error attribute to mark operation as failed
	for _, a := range attrs {
		if a.Key == "error" && a.Value.AsString() != "" {
			op.success = false
			op.failure = fmt.Errorf("%s", a.Value.AsString())
			if op.span != nil {
				op.span.RecordError(op.failure)
			}
		}
	}
}

// markSuccess marks the operation as successful.
func (op *operationState) markSuccess() {
	op.mu.Lock()
	defer op.mu.Unlock()
	op.success = true
	op.failure = nil
}

// markFailure marks the operation as failed.
func (op *operationState) markFailure(err error) {
	op.mu.Lock()
	defer op.mu.Unlock()
	op.success = false
	op.failure = err

	if op.span != nil && err != nil {
		op.span.RecordError(err)
	}
}

// buildMetricLabels builds the metric labels from registered names.
// If a label name was registered but no attribute with that key exists, uses "_".
// Static attributes are automatically included as labels.
func (op *operationState) buildMetricLabels() []attr.Attr {
	op.mu.Lock()
	defer op.mu.Unlock()

	// Start with static attributes
	labels := make([]attr.Attr, 0, len(op.metricLabels)+op.bedrock.staticAttr.Len())

	op.bedrock.staticAttr.Range(func(a attr.Attr) bool {
		labels = append(labels, a)
		return true
	})

	// Add operation-specific labels
	for _, labelName := range op.metricLabels {
		found := false
		op.attrs.Range(func(a attr.Attr) bool {
			if a.Key == labelName {
				labels = append(labels, a)
				found = true
				return false // stop iteration
			}
			return true
		})

		if !found {
			// Use "_" as default value for missing labels
			labels = append(labels, attr.String(labelName, "_"))
		}
	}

	return labels
}

// recordMetrics records all automatic metrics for this operation.
func (op *operationState) recordMetrics() {
	if op.bedrock.isNoop {
		return
	}

	duration := time.Since(op.startTime)
	labels := op.buildMetricLabels()

	// Build combined label names (static + operation-specific)
	staticLabelNames := make([]string, 0, op.bedrock.staticAttr.Len())
	op.bedrock.staticAttr.Range(func(a attr.Attr) bool {
		staticLabelNames = append(staticLabelNames, a.Key)
		return true
	})

	allLabelNames := append(staticLabelNames, op.metricLabels...)

	// Record count
	counter := op.bedrock.metrics.Counter(
		op.name+"_count",
		"Total count of "+op.name+" operations",
		allLabelNames...,
	)
	counter.With(labels...).Inc()

	// Record success or failure
	if op.success {
		successCounter := op.bedrock.metrics.Counter(
			op.name+"_successes",
			"Successful "+op.name+" operations",
			allLabelNames...,
		)
		successCounter.With(labels...).Inc()
	} else {
		failureCounter := op.bedrock.metrics.Counter(
			op.name+"_failures",
			"Failed "+op.name+" operations",
			allLabelNames...,
		)
		failureCounter.With(labels...).Inc()
	}

	// Record duration in milliseconds
	histogram := op.bedrock.metrics.Histogram(
		op.name+"_duration_ms",
		"Duration of "+op.name+" operations in milliseconds",
		nil, // Use default buckets
		allLabelNames...,
	)
	histogram.With(labels...).Observe(float64(duration.Milliseconds()))
}

// end finishes the operation.
func (op *operationState) end() {
	// End the span
	if op.span != nil {
		op.span.End()
	}

	// Record metrics
	op.recordMetrics()

	// Canonical log if enabled
	if op.bedrock.config.LogCanonical && !op.bedrock.isNoop {
		op.logCanonical()
	}
}

// logCanonical writes a structured log of the complete operation.
func (op *operationState) logCanonical() {
	op.mu.Lock()
	defer op.mu.Unlock()

	duration := time.Since(op.startTime)

	// Collect attributes
	attrs := make(map[string]interface{})
	op.attrs.Range(func(a attr.Attr) bool {
		attrs[a.Key] = a.Value.AsAny()
		return true
	})

	// Collect step information
	steps := make([]map[string]interface{}, len(op.steps))
	for i, step := range op.steps {
		stepAttrs := make(map[string]interface{})
		step.attrs.Range(func(a attr.Attr) bool {
			stepAttrs[a.Key] = a.Value.AsAny()
			return true
		})
		steps[i] = map[string]interface{}{
			"name":       step.span.Name(),
			"attributes": stepAttrs,
		}
	}

	// Build log fields
	logFields := []interface{}{
		"operation", op.name,
		"duration_ms", duration.Milliseconds(),
		"success", op.success,
	}

	if op.failure != nil {
		logFields = append(logFields, "error", op.failure.Error())
	}

	if len(attrs) > 0 {
		logFields = append(logFields, "attributes", attrs)
	}

	if len(steps) > 0 {
		logFields = append(logFields, "steps", steps)
	}

	op.bedrock.logger.Info("operation.complete", logFields...)
}

// StepFromContext creates a lightweight step within an operation for tracing without full operation metrics.
// Steps are part of their parent operation and contribute attributes/events to it.
// Use this for helper functions where you want trace visibility but not separate metrics.
//
// Usage:
//
//	step := bedrock.Step(ctx, "helper")
//	defer step.Done()
func StepFromContext(ctx context.Context, name string, attrs ...attr.Attr) *OpStep {
	b := bedrockFromContext(ctx)

	// Get parent operation
	parent := operationStateFromContext(ctx)

	// Enumerate step name if multiple steps with same name
	fullName := name
	if parent != nil {
		parent.mu.Lock()
		count := parent.stepCounts[name]
		parent.stepCounts[name] = count + 1
		if count > 0 {
			fullName = fmt.Sprintf("%s[%d]", name, count)
		}
		parent.mu.Unlock()
	}

	var parentCtx context.Context
	if parent != nil && parent.span != nil {
		parentCtx = trace.ContextWithSpan(ctx, parent.span)
	} else {
		parentCtx = ctx
	}

	_, span := b.tracer.Start(parentCtx, fullName, trace.WithAttrs(attrs...))

	step := &OpStep{
		span:   span,
		attrs:  attr.NewSet(attrs...),
		parent: parent,
		ctx:    ctx,
	}

	// Track step in parent
	if parent != nil {
		parent.mu.Lock()
		parent.steps = append(parent.steps, step)
		parent.mu.Unlock()
	}

	return step
}

// Register adds attributes or events to the step.
// Attributes are propagated to the parent operation.
// Events are recorded in traces.
//
// Usage:
//
//	step.Register(ctx,
//	    attr.String("rows", "42"),
//	    attr.NewEvent("query.complete"),
//	)
func (s *OpStep) Register(ctx context.Context, items ...interface{}) {
	attrs := make([]attr.Attr, 0)

	for _, item := range items {
		switch v := item.(type) {
		case attr.Attr:
			attrs = append(attrs, v)
		case attr.Event:
			// Register as trace event
			if s.span != nil {
				s.span.AddEvent(v.Name, v.Attrs...)
			}
		}
	}

	if len(attrs) > 0 {
		if s.span != nil {
			s.span.SetAttr(attrs...)
		}
		s.attrs = s.attrs.Merge(attrs...)

		// Propagate to parent operation
		if s.parent != nil {
			s.parent.setAttr(attrs...)
		}
	}
}

// Done ends the step.
func (s *OpStep) Done() {
	if s.span != nil {
		s.span.End()
	}
}
