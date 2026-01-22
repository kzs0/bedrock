package trace

import (
	"sync"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/internal"
)

// SpanKind represents the role of a span in a trace.
type SpanKind int

const (
	SpanKindInternal SpanKind = iota
	SpanKindServer
	SpanKindClient
	SpanKindProducer
	SpanKindConsumer
)

// SpanStatus represents the status of a span.
type SpanStatus int

const (
	StatusUnset SpanStatus = iota
	StatusOK
	StatusError
)

// Span represents a single operation within a trace.
type Span struct {
	mu sync.Mutex

	name       string
	traceID    internal.TraceID
	spanID     internal.SpanID
	parentID   internal.SpanID
	kind       SpanKind
	startTime  time.Time
	endTime    time.Time
	attrs      attr.Set
	events     []Event
	status     SpanStatus
	statusMsg  string
	tracestate string // W3C tracestate for propagation

	tracer *Tracer
	ended  bool
}

// Event represents an event within a span.
type Event struct {
	Name  string
	Time  time.Time
	Attrs attr.Set
}

// TraceID returns the trace ID.
func (s *Span) TraceID() internal.TraceID {
	return s.traceID
}

// SpanID returns the span ID.
func (s *Span) SpanID() internal.SpanID {
	return s.spanID
}

// ParentID returns the parent span ID.
func (s *Span) ParentID() internal.SpanID {
	return s.parentID
}

// Name returns the span name.
func (s *Span) Name() string {
	return s.name
}

// Kind returns the span kind.
func (s *Span) Kind() SpanKind {
	return s.kind
}

// StartTime returns the span start time.
func (s *Span) StartTime() time.Time {
	return s.startTime
}

// EndTime returns the span end time.
func (s *Span) EndTime() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endTime
}

// Attrs returns the span attributes.
func (s *Span) Attrs() attr.Set {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attrs
}

// Events returns the span events.
func (s *Span) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := make([]Event, len(s.events))
	copy(events, s.events)
	return events
}

// Status returns the span status.
func (s *Span) Status() (SpanStatus, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status, s.statusMsg
}

// SetAttr adds or updates attributes on the span.
func (s *Span) SetAttr(attrs ...attr.Attr) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ended {
		return
	}
	s.attrs = s.attrs.Merge(attrs...)
}

// AddEvent adds an event to the span.
func (s *Span) AddEvent(name string, attrs ...attr.Attr) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ended {
		return
	}
	s.events = append(s.events, Event{
		Name:  name,
		Time:  time.Now(),
		Attrs: attr.NewSet(attrs...),
	})
}

// RecordError records an error as an event and sets the span status.
func (s *Span) RecordError(err error, attrs ...attr.Attr) {
	if err == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ended {
		return
	}

	errAttrs := append([]attr.Attr{
		attr.String("exception.type", "error"),
		attr.String("exception.message", err.Error()),
	}, attrs...)

	s.events = append(s.events, Event{
		Name:  "exception",
		Time:  time.Now(),
		Attrs: attr.NewSet(errAttrs...),
	})

	s.status = StatusError
	s.statusMsg = err.Error()
}

// SetStatus sets the span status.
func (s *Span) SetStatus(status SpanStatus, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ended {
		return
	}
	s.status = status
	s.statusMsg = msg
}

// End finishes the span and exports it.
func (s *Span) End() {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.endTime = time.Now()
	s.ended = true
	s.mu.Unlock()

	if s.tracer != nil {
		s.tracer.export(s)
	}
}

// IsRecording returns true if the span is recording events.
func (s *Span) IsRecording() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.ended
}

// Duration returns the span duration.
func (s *Span) Duration() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.endTime.IsZero() {
		return time.Since(s.startTime)
	}
	return s.endTime.Sub(s.startTime)
}
