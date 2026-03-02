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
	noop   bool // true = pre-allocated singleton; all methods fast-return
	name   string
	span   *trace.Span
	attrs  attr.Set
	parent *operationState
	ctx    context.Context
}

// operationState is the internal state of an operation.
// This is stored in the context and should not be exposed to users.
type operationState struct {
	mu sync.Mutex

	bedrock       *Bedrock
	span          *trace.Span
	name          string
	startTime     time.Time
	attrs         attr.Set
	metricLabels  []string // defined label names (upfront registration)
	cachedMetrics *cachedOpMetrics
	parent        *operationState
	success       bool
	failure       error

	// Child tracking
	steps []*OpStep
}

// newOperationState creates a new operation state.
func newOperationState(b *Bedrock, span *trace.Span, name string, cfg operationConfig, parent *operationState) *operationState {
	var cm *cachedOpMetrics
	if !b.isNoop {
		cm = b.getOpMetrics(name, cfg.metricLabels)
	}
	return &operationState{
		bedrock:       b,
		span:          span,
		name:          name,
		startTime:     time.Now(),
		attrs:         attr.NewSet(cfg.attrs...),
		metricLabels:  cfg.metricLabels,
		cachedMetrics: cm,
		parent:        parent,
		success:       true, // Default to success
	}
}

// setAttr adds or updates attributes on the operation.
// Span attrs are not updated here; they are synced in bulk at end() to avoid
// a Merge allocation per Register call.
func (op *operationState) setAttr(attrs ...attr.Attr) {
	op.mu.Lock()
	defer op.mu.Unlock()

	op.attrs = op.attrs.Merge(attrs...)

	// Check for error attribute to mark operation as failed and expand details.
	for _, a := range attrs {
		if a.Key != "error" {
			continue
		}
		switch a.Value.Kind() {
		case attr.KindError:
			det := a.Value.AsError()
			if det == nil || det.Err() == nil {
				continue
			}
			op.success = false
			op.failure = det.Err()
			if op.span != nil {
				op.span.RecordError(op.failure)
			}
			// Expand into structured sub-attributes for rich error context.
			extra := make([]attr.Attr, 0, 5)
			extra = append(extra, attr.String("error.type", det.TypeName()))
			extra = append(extra, attr.String("error.message", det.Err().Error()))
			if stack := det.FormatStack(); stack != "" {
				extra = append(extra, attr.String("error.stack", stack))
			}
			if chain := det.FormatChain(); chain != "" {
				extra = append(extra, attr.String("error.chain", chain))
			}
			extra = append(extra, attr.String("error.fingerprint", det.Fingerprint()))
			op.attrs = op.attrs.Merge(extra...)
		case attr.KindString:
			// Legacy path: plain string error value.
			if a.Value.String() != "" {
				op.success = false
				op.failure = fmt.Errorf("%s", a.Value.String())
				if op.span != nil {
					op.span.RecordError(op.failure)
				}
			}
		}
	}
}

// buildMetricLabels resolves runtime label values from the operation's attrs.
// If a label name was registered but no attribute with that key exists, uses "_".
// Static label values come from the pre-cached Bedrock.staticLabelVals.
func (op *operationState) buildMetricLabels() []attr.Attr {
	op.mu.Lock()
	defer op.mu.Unlock()

	nStatic := len(op.bedrock.staticLabelVals)
	labels := make([]attr.Attr, nStatic, nStatic+len(op.metricLabels))
	copy(labels, op.bedrock.staticLabelVals)

	// Add operation-specific labels (search operation attrs first, then step attrs)
	for _, labelName := range op.metricLabels {
		found := false

		// First check operation attributes
		op.attrs.Range(func(a attr.Attr) bool {
			if a.Key == labelName {
				labels = append(labels, a)
				found = true
				return false // stop iteration
			}
			return true
		})

		// If not found, check step attributes
		if !found {
			for _, step := range op.steps {
				step.attrs.Range(func(a attr.Attr) bool {
					if a.Key == labelName {
						labels = append(labels, a)
						found = true
						return false // stop iteration
					}
					return true
				})
				if found {
					break
				}
			}
		}

		if !found {
			labels = append(labels, attr.String(labelName, "_"))
		}
	}

	return labels
}

// recordMetrics records all automatic metrics for this operation.
func (op *operationState) recordMetrics() {
	if op.bedrock.isNoop || op.cachedMetrics == nil {
		return
	}

	duration := time.Since(op.startTime)
	labels := op.buildMetricLabels()
	cm := op.cachedMetrics

	cm.count.With(labels...).Inc()

	if op.success {
		cm.success.With(labels...).Inc()
	} else {
		cm.failure.With(labels...).Inc()
	}

	cm.duration.With(labels...).Observe(float64(duration.Milliseconds()))
}

// end finishes the operation.
func (op *operationState) end() {
	// Capture duration before any other work so the logged value matches
	// the actual operation wall time rather than including log/metric overhead.
	duration := time.Since(op.startTime)

	// Sync accumulated attrs to span in one shot before ending, to avoid
	// a Merge allocation on every Register call during the operation lifetime.
	if op.span != nil {
		op.mu.Lock()
		finalAttrs := op.attrs
		op.mu.Unlock()
		op.span.SetAttrSet(finalAttrs)
		op.span.End()
	}

	// Record metrics
	op.recordMetrics()

	// Canonical log if enabled
	if op.bedrock.config.LogCanonical && !op.bedrock.isNoop {
		op.logCanonical(duration)
	}
}

// logCanonical writes a structured log of the complete operation.
func (op *operationState) logCanonical(duration time.Duration) {
	op.mu.Lock()
	defer op.mu.Unlock()

	// Collect attributes
	attrs := make(map[string]any)
	op.attrs.Range(func(a attr.Attr) bool {
		attrs[a.Key] = a.Value.AsAny()
		return true
	})

	// Collect step information
	steps := make([]map[string]any, len(op.steps))
	for i, step := range op.steps {
		stepAttrs := make(map[string]any)
		step.attrs.Range(func(a attr.Attr) bool {
			stepAttrs[a.Key] = a.Value.AsAny()
			return true
		})
		steps[i] = map[string]any{
			"name":       step.name,
			"attributes": stepAttrs,
		}
	}

	// Build log fields
	logFields := []any{
		"operation", op.name,
		"duration_ms", duration.Milliseconds(),
		"success", op.success,
	}

	if op.failure != nil {
		logFields = append(logFields, "error", op.failure.Error())
		// Include rich error details if captured.
		if v, ok := op.attrs.Get("error.type"); ok {
			logFields = append(logFields, "error.type", v.String())
		}
		if v, ok := op.attrs.Get("error.fingerprint"); ok {
			logFields = append(logFields, "error.fingerprint", v.String())
		}
		if v, ok := op.attrs.Get("error.stack"); ok {
			logFields = append(logFields, "error.stack", v.String())
		}
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
func StepFromContext(ctx context.Context, name string, opts ...StepOption) *OpStep {
	b := bedrockFromContext(ctx)
	if b.isNoop {
		return globalNoopStep
	}
	cfg := applyStepOptions(opts)

	// Get parent operation
	parent := operationStateFromContext(ctx)

	var span *trace.Span

	// Skip tracing if no-trace mode is active (from context or step option)
	if !isNoTrace(ctx) && !cfg.noTrace {
		var parentSpan *trace.Span
		if parent != nil {
			parentSpan = parent.span
		}
		_, span = b.tracer.StartSpan(ctx, name, parentSpan, nil, cfg.attrs)
	}

	step := &OpStep{
		name:   name,
		span:   span,
		attrs:  attr.NewSet(cfg.attrs...),
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

// Register adds attributes to the step.
// Attributes remain on the step but can be used as metric label values for the parent operation.
//
// Usage:
//
//	step.Register(ctx,
//	    attr.String("rows", "42"),
//	)
func (s *OpStep) Register(ctx context.Context, attrs ...attr.Attr) {
	if s.noop || len(attrs) == 0 {
		return
	}
	s.attrs = s.attrs.Merge(attrs...)
}

// Event records a trace event on the step span.
//
// Usage:
//
//	step.Event(ctx, attr.NewEvent("query.complete"))
func (s *OpStep) Event(ctx context.Context, event attr.Event) {
	if s.span != nil {
		s.span.AddEvent(event.Name, event.Attrs...)
	}
}

// Done ends the step, syncing accumulated attrs to the span in one shot.
func (s *OpStep) Done() {
	if s.noop || s.span == nil {
		return
	}
	s.span.SetAttrSet(s.attrs)
	s.span.End()
}
