package trace

import (
	"context"
	"sync"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/internal"
)

// Exporter exports finished spans.
type Exporter interface {
	ExportSpans(ctx context.Context, spans []*Span) error
	Shutdown(ctx context.Context) error
}

// Tracer creates spans and manages trace context.
type Tracer struct {
	mu          sync.Mutex
	serviceName string
	resource    attr.Set
	sampler     Sampler
	exporter    Exporter
}

// TracerConfig configures the tracer.
type TracerConfig struct {
	ServiceName string
	Resource    attr.Set
	Sampler     Sampler
	Exporter    Exporter
}

// NewTracer creates a new tracer.
func NewTracer(cfg TracerConfig) *Tracer {
	sampler := cfg.Sampler
	if sampler == nil {
		sampler = AlwaysSampler{}
	}

	return &Tracer{
		serviceName: cfg.ServiceName,
		resource:    cfg.Resource,
		sampler:     sampler,
		exporter:    cfg.Exporter,
	}
}

// StartSpanOptions configures span creation.
type StartSpanOptions struct {
	Kind   SpanKind
	Attrs  []attr.Attr
	Parent *Span
}

// Start creates a new span.
func (t *Tracer) Start(ctx context.Context, name string, opts ...StartSpanOption) (context.Context, *Span) {
	var options StartSpanOptions
	for _, opt := range opts {
		opt(&options)
	}

	// Get parent span from context if not explicitly provided
	parent := options.Parent
	if parent == nil {
		parent = SpanFromContext(ctx)
	}

	var traceID internal.TraceID
	var parentID internal.SpanID
	var parentSampled bool

	if parent != nil {
		traceID = parent.traceID
		parentID = parent.spanID
		parentSampled = true // If parent exists and wasn't dropped, it was sampled
	} else {
		traceID = internal.NewTraceID()
	}

	// Check sampling decision
	result := t.sampler.ShouldSample(traceID, name, parentSampled)
	if result.Decision == SamplingDecisionDrop {
		// Return a no-op span
		noopSpan := &Span{
			name:      name,
			traceID:   traceID,
			spanID:    internal.NewSpanID(),
			parentID:  parentID,
			startTime: time.Now(),
			ended:     true, // Mark as ended so it's not exported
		}
		return ContextWithSpan(ctx, noopSpan), noopSpan
	}

	span := &Span{
		name:      name,
		traceID:   traceID,
		spanID:    internal.NewSpanID(),
		parentID:  parentID,
		kind:      options.Kind,
		startTime: time.Now(),
		attrs:     attr.NewSet(options.Attrs...),
		tracer:    t,
	}

	return ContextWithSpan(ctx, span), span
}

// export sends a completed span to the exporter.
func (t *Tracer) export(span *Span) {
	if t.exporter == nil {
		return
	}
	// Export asynchronously to not block the caller
	go t.exporter.ExportSpans(context.Background(), []*Span{span})
}

// Shutdown shuts down the tracer and flushes any pending spans.
func (t *Tracer) Shutdown(ctx context.Context) error {
	if t.exporter != nil {
		return t.exporter.Shutdown(ctx)
	}
	return nil
}

// ServiceName returns the service name.
func (t *Tracer) ServiceName() string {
	return t.serviceName
}

// Resource returns the resource attributes.
func (t *Tracer) Resource() attr.Set {
	return t.resource
}

// StartSpanOption configures span creation.
type StartSpanOption func(*StartSpanOptions)

// WithSpanKind sets the span kind.
func WithSpanKind(kind SpanKind) StartSpanOption {
	return func(o *StartSpanOptions) {
		o.Kind = kind
	}
}

// WithAttrs sets the initial span attributes.
func WithAttrs(attrs ...attr.Attr) StartSpanOption {
	return func(o *StartSpanOptions) {
		o.Attrs = attrs
	}
}

// WithParent sets the parent span.
func WithParent(parent *Span) StartSpanOption {
	return func(o *StartSpanOptions) {
		o.Parent = parent
	}
}
