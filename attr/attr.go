package attr

import (
	"time"
)

// Attr is a key-value pair for observability attributes.
type Attr struct {
	Key   string
	Value Value
}

// registrable implements the Registrable interface.
func (Attr) registrable() {}

// String creates a string attribute.
func String(key, value string) Attr {
	return Attr{Key: key, Value: StringValue(value)}
}

// Int creates an int attribute (stored as int64).
func Int(key string, value int) Attr {
	return Attr{Key: key, Value: Int64Value(int64(value))}
}

// Int64 creates an int64 attribute.
func Int64(key string, value int64) Attr {
	return Attr{Key: key, Value: Int64Value(value)}
}

// Uint64 creates a uint64 attribute.
func Uint64(key string, value uint64) Attr {
	return Attr{Key: key, Value: Uint64Value(value)}
}

// Float64 creates a float64 attribute.
func Float64(key string, value float64) Attr {
	return Attr{Key: key, Value: Float64Value(value)}
}

// Bool creates a bool attribute.
func Bool(key string, value bool) Attr {
	return Attr{Key: key, Value: BoolValue(value)}
}

// Duration creates a time.Duration attribute.
func Duration(key string, value time.Duration) Attr {
	return Attr{Key: key, Value: DurationValue(value)}
}

// Time creates a time.Time attribute.
func Time(key string, value time.Time) Attr {
	return Attr{Key: key, Value: TimeValue(value)}
}

// Any creates an attribute from any value.
func Any(key string, value any) Attr {
	return Attr{Key: key, Value: AnyValue(value)}
}

// Error creates an attribute for an error.
func Error(err error) Attr {
	if err == nil {
		return Attr{Key: "error", Value: StringValue("")}
	}
	return Attr{Key: "error", Value: StringValue(err.Error())}
}

// Registrable represents an item that can be registered on operations and steps.
// This includes attributes (which become span attributes and operation state)
// and events (which become span events).
type Registrable interface {
	registrable() // private marker method
}

// Aggregation represents an aggregation operation for source metrics.
// Aggregations are used by sources to track accumulated values over time.
type Aggregation interface {
	aggregation() // private marker method
}

// SumAttr represents an aggregation attribute for summing values.
// Used to track cumulative totals (e.g., total requests, total bytes).
type SumAttr struct {
	Key   string
	Value float64
}

func (SumAttr) aggregation() {}

// Sum creates a sum aggregation attribute.
// This is used for sources to aggregate metrics as counters.
func Sum(key string, value float64) SumAttr {
	return SumAttr{Key: key, Value: value}
}

// GaugeAttr represents a gauge aggregation for tracking current values.
// Used to track values that can go up or down (e.g., active connections, queue depth).
type GaugeAttr struct {
	Key   string
	Value float64
}

func (GaugeAttr) aggregation() {}

// Gauge creates a gauge aggregation attribute.
// This is used for sources to set gauge values.
func Gauge(key string, value float64) GaugeAttr {
	return GaugeAttr{Key: key, Value: value}
}

// HistogramAttr represents a histogram observation for tracking distributions.
// Used to track distributions of values (e.g., request durations, response sizes).
type HistogramAttr struct {
	Key   string
	Value float64
}

func (HistogramAttr) aggregation() {}

// Histogram creates a histogram aggregation attribute.
// This is used for sources to record histogram observations.
func Histogram(key string, value float64) HistogramAttr {
	return HistogramAttr{Key: key, Value: value}
}

// Event represents a trace event with attributes.
type Event struct {
	Name  string
	Attrs []Attr
}

// registrable implements the Registrable interface.
func (Event) registrable() {}

// NewEvent creates an event with attributes.
// Events are recorded in traces but don't become operation attributes.
func NewEvent(name string, attrs ...Attr) Event {
	return Event{
		Name:  name,
		Attrs: attrs,
	}
}

// String returns a string representation of the attribute.
func (a Attr) String() string {
	return a.Key + "=" + a.Value.String()
}

// WithKey returns a new attribute with the given key.
func (a Attr) WithKey(key string) Attr {
	return Attr{Key: key, Value: a.Value}
}
