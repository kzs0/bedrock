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

// Error creates an attribute for an error. It captures the error type, a stack
// trace at the call site, and the error chain. Operations that receive this
// attribute are automatically marked as failed. The stack and type information
// is expanded into additional span attributes (error.type, error.stack,
// error.chain, error.fingerprint) when registered with an operation.
func Error(err error) Attr {
	if err == nil {
		return Attr{Key: "error", Value: StringValue("")}
	}
	return Attr{Key: "error", Value: ErrorValue(err, 1)}
}

// ErrorWithStack creates an error attribute like Error but lets the caller
// control how many additional stack frames to skip. Use this when wrapping
// attr.Error in helper functions to point the stack to the real call site.
//
// A skip of 0 is equivalent to calling attr.Error directly.
func ErrorWithStack(err error, skip int) Attr {
	if err == nil {
		return Attr{Key: "error", Value: StringValue("")}
	}
	return Attr{Key: "error", Value: ErrorValue(err, skip+1)}
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

// WithKey returns a new attribute with the given key.
func (a Attr) WithKey(key string) Attr {
	return Attr{Key: key, Value: a.Value}
}
