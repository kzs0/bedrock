package attr

import (
	"time"
)

// Attr is a key-value pair for observability attributes.
type Attr struct {
	Key   string
	Value Value
}

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

// SumAttr represents an aggregation attribute for summing values.
type SumAttr struct {
	Key   string
	Value float64
}

// Sum creates a sum aggregation attribute.
// This is used for sources to aggregate metrics.
func Sum(key string, value float64) SumAttr {
	return SumAttr{Key: key, Value: value}
}

// Event represents a trace event with attributes.
type Event struct {
	Name  string
	Attrs []Attr
}

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
