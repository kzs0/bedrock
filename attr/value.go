package attr

import (
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"math"
	"reflect"
	"runtime"
	"strings"
	"time"
)

// Kind represents the type of a Value.
type Kind int

const (
	KindString Kind = iota
	KindInt64
	KindUint64
	KindFloat64
	KindBool
	KindDuration
	KindTime
	KindAny
	KindError
)

// Value is a union type that can hold any attribute value efficiently.
// Basic types (int64, uint64, float64, bool, duration) are stored inline
// without allocation.
type Value struct {
	kind Kind
	num  uint64
	str  string
	any  any
}

// Kind returns the type of the value.
func (v Value) Kind() Kind {
	return v.kind
}

// StringValue creates a Value from a string.
func StringValue(s string) Value {
	return Value{kind: KindString, str: s}
}

// Int64Value creates a Value from an int64.
func Int64Value(n int64) Value {
	return Value{kind: KindInt64, num: uint64(n)}
}

// Uint64Value creates a Value from a uint64.
func Uint64Value(n uint64) Value {
	return Value{kind: KindUint64, num: n}
}

// Float64Value creates a Value from a float64.
func Float64Value(f float64) Value {
	return Value{kind: KindFloat64, num: float64Bits(f)}
}

// BoolValue creates a Value from a bool.
func BoolValue(b bool) Value {
	var n uint64
	if b {
		n = 1
	}
	return Value{kind: KindBool, num: n}
}

// DurationValue creates a Value from a time.Duration.
func DurationValue(d time.Duration) Value {
	return Value{kind: KindDuration, num: uint64(d)}
}

// TimeValue creates a Value from a time.Time.
// The time is stored as Unix nanoseconds (UTC); sub-nanosecond precision
// and original timezone are not preserved, which is fine for observability.
func TimeValue(t time.Time) Value {
	return Value{kind: KindTime, num: uint64(t.UnixNano())}
}

// AnyValue creates a Value from any type.
func AnyValue(v any) Value {
	switch val := v.(type) {
	case string:
		return StringValue(val)
	case int:
		return Int64Value(int64(val))
	case int64:
		return Int64Value(val)
	case uint64:
		return Uint64Value(val)
	case float64:
		return Float64Value(val)
	case bool:
		return BoolValue(val)
	case time.Duration:
		return DurationValue(val)
	case time.Time:
		return TimeValue(val)
	default:
		return Value{kind: KindAny, any: v}
	}
}

// AsString returns the value as a string. Panics if kind != KindString.
func (v Value) AsString() string {
	if v.kind != KindString {
		panic("Value.AsString: not a string")
	}
	return v.str
}

// AsInt64 returns the value as an int64. Panics if kind != KindInt64.
func (v Value) AsInt64() int64 {
	if v.kind != KindInt64 {
		panic("Value.AsInt64: not an int64")
	}
	return int64(v.num)
}

// AsUint64 returns the value as a uint64. Panics if kind != KindUint64.
func (v Value) AsUint64() uint64 {
	if v.kind != KindUint64 {
		panic("Value.AsUint64: not a uint64")
	}
	return v.num
}

// AsFloat64 returns the value as a float64. Panics if kind != KindFloat64.
func (v Value) AsFloat64() float64 {
	if v.kind != KindFloat64 {
		panic("Value.AsFloat64: not a float64")
	}
	return float64FromBits(v.num)
}

// AsBool returns the value as a bool. Panics if kind != KindBool.
func (v Value) AsBool() bool {
	if v.kind != KindBool {
		panic("Value.AsBool: not a bool")
	}
	return v.num != 0
}

// AsDuration returns the value as a time.Duration. Panics if kind != KindDuration.
func (v Value) AsDuration() time.Duration {
	if v.kind != KindDuration {
		panic("Value.AsDuration: not a duration")
	}
	return time.Duration(v.num)
}

// AsTime returns the value as a time.Time (UTC). Panics if kind != KindTime.
func (v Value) AsTime() time.Time {
	if v.kind != KindTime {
		panic("Value.AsTime: not a time")
	}
	return time.Unix(0, int64(v.num)).UTC()
}

// AsAny returns the underlying value as an any.
func (v Value) AsAny() any {
	switch v.kind {
	case KindString:
		return v.str
	case KindInt64:
		return int64(v.num)
	case KindUint64:
		return v.num
	case KindFloat64:
		return float64FromBits(v.num)
	case KindBool:
		return v.num != 0
	case KindDuration:
		return time.Duration(v.num)
	case KindTime:
		return time.Unix(0, int64(v.num)).UTC()
	case KindError:
		if d, ok := v.any.(*errorDetail); ok && d.err != nil {
			return d.err.Error()
		}
		return ""
	default:
		return v.any
	}
}

// String returns a string representation of the value.
func (v Value) String() string {
	switch v.kind {
	case KindString:
		return v.str
	case KindInt64:
		return fmt.Sprintf("%d", int64(v.num))
	case KindUint64:
		return fmt.Sprintf("%d", v.num)
	case KindFloat64:
		return fmt.Sprintf("%g", float64FromBits(v.num))
	case KindBool:
		if v.num != 0 {
			return "true"
		}
		return "false"
	case KindDuration:
		return time.Duration(v.num).String()
	case KindTime:
		return time.Unix(0, int64(v.num)).UTC().Format(time.RFC3339Nano)
	case KindError:
		if d, ok := v.any.(*errorDetail); ok && d.err != nil {
			return d.err.Error()
		}
		return ""
	default:
		return fmt.Sprintf("%v", v.any)
	}
}

// float64Bits converts a float64 to its bit representation.
func float64Bits(f float64) uint64 {
	return math.Float64bits(f)
}

// float64FromBits converts a bit representation to float64.
func float64FromBits(b uint64) float64 {
	return math.Float64frombits(b)
}

// errorDetail holds structured information about an error, captured at the call site.
type errorDetail struct {
	err      error
	typeName string    // reflect.TypeOf(err).String()
	pcs      []uintptr // raw program counters from runtime.Callers
}

// Err returns the underlying error.
func (d *errorDetail) Err() error { return d.err }

// TypeName returns the Go type name of the error.
func (d *errorDetail) TypeName() string { return d.typeName }

// ErrorValue creates a Value that carries a structured error with stack trace.
// skip controls how many call frames to skip above ErrorValue itself.
func ErrorValue(err error, skip int) Value {
	if err == nil {
		return StringValue("")
	}
	const maxFrames = 32
	pcs := make([]uintptr, maxFrames)
	n := runtime.Callers(skip+2, pcs)
	pcs = pcs[:n]

	typeName := reflect.TypeOf(err).String()
	return Value{kind: KindError, any: &errorDetail{err: err, typeName: typeName, pcs: pcs}}
}

// AsError returns the error detail. Returns nil if the value is not a KindError.
func (v Value) AsError() *errorDetail {
	if v.kind != KindError {
		return nil
	}
	d, _ := v.any.(*errorDetail)
	return d
}

// FormatStack formats the captured stack trace as "funcName (file:line)\n..." strings,
// skipping runtime-internal frames, up to 32 frames.
func (d *errorDetail) FormatStack() string {
	if len(d.pcs) == 0 {
		return ""
	}
	frames := runtime.CallersFrames(d.pcs)
	var sb strings.Builder
	for {
		f, more := frames.Next()
		if f.Function == "" {
			break
		}
		// Skip runtime internals
		if strings.HasPrefix(f.Function, "runtime.") {
			if more {
				continue
			}
			break
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(f.Function)
		sb.WriteString(" (")
		sb.WriteString(f.File)
		sb.WriteByte(':')
		fmt.Fprintf(&sb, "%d", f.Line)
		sb.WriteByte(')')
		if !more {
			break
		}
	}
	return sb.String()
}

// errorLink represents one entry in an error chain.
type errorLink struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// FormatChain walks the Unwrap() error chain and returns a JSON array of
// {type, message} objects. Returns "" if there is no wrapped error.
func (d *errorDetail) FormatChain() string {
	type unwrapper interface{ Unwrap() error }
	var chain []errorLink
	cur := d.err
	for {
		u, ok := cur.(unwrapper)
		if !ok {
			break
		}
		inner := u.Unwrap()
		if inner == nil {
			break
		}
		chain = append(chain, errorLink{
			Type:    reflect.TypeOf(inner).String(),
			Message: inner.Error(),
		})
		cur = inner
	}
	if len(chain) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, l := range chain {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`{"type":`)
		sb.WriteString(jsonString(l.Type))
		sb.WriteString(`,"message":`)
		sb.WriteString(jsonString(l.Message))
		sb.WriteByte('}')
	}
	sb.WriteByte(']')
	return sb.String()
}

// Fingerprint produces a stable 16-char hex fingerprint by hashing the error
// type name and the top 3 non-runtime stack frame PCs. Two errors at the same
// call site with the same type produce the same fingerprint.
func (d *errorDetail) Fingerprint() string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(d.typeName))
	frames := runtime.CallersFrames(d.pcs)
	count := 0
	for count < 3 {
		f, more := frames.Next()
		if f.Function == "" {
			break
		}
		if strings.HasPrefix(f.Function, "runtime.") {
			if !more {
				break
			}
			continue
		}
		_, _ = h.Write([]byte(f.Function))
		count++
		if !more {
			break
		}
	}
	b := h.Sum(nil)
	return hex.EncodeToString(b)
}

// jsonString returns a JSON-encoded string (basic escaping).
func jsonString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return `"` + s + `"`
}
