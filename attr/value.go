package attr

import (
	"fmt"
	"math"
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
func TimeValue(t time.Time) Value {
	return Value{kind: KindTime, any: t}
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

// AsTime returns the value as a time.Time. Panics if kind != KindTime.
func (v Value) AsTime() time.Time {
	if v.kind != KindTime {
		panic("Value.AsTime: not a time")
	}
	return v.any.(time.Time)
}

// AsAny returns the underlying value as an interface{}.
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
		return v.any.(time.Time)
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
		return v.any.(time.Time).Format(time.RFC3339Nano)
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
