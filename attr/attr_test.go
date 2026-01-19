package attr

import (
	"testing"
	"time"
)

func TestAttrString(t *testing.T) {
	a := String("key", "value")
	if a.Key != "key" {
		t.Errorf("expected key 'key', got %q", a.Key)
	}
	if a.Value.Kind() != KindString {
		t.Errorf("expected KindString, got %v", a.Value.Kind())
	}
	if a.Value.AsString() != "value" {
		t.Errorf("expected 'value', got %q", a.Value.AsString())
	}
}

func TestAttrInt(t *testing.T) {
	a := Int("count", 42)
	if a.Value.Kind() != KindInt64 {
		t.Errorf("expected KindInt64, got %v", a.Value.Kind())
	}
	if a.Value.AsInt64() != 42 {
		t.Errorf("expected 42, got %d", a.Value.AsInt64())
	}
}

func TestAttrInt64(t *testing.T) {
	a := Int64("big", int64(1<<62))
	if a.Value.AsInt64() != int64(1<<62) {
		t.Errorf("expected %d, got %d", int64(1<<62), a.Value.AsInt64())
	}
}

func TestAttrFloat64(t *testing.T) {
	a := Float64("pi", 3.14159)
	if a.Value.Kind() != KindFloat64 {
		t.Errorf("expected KindFloat64, got %v", a.Value.Kind())
	}
	if a.Value.AsFloat64() != 3.14159 {
		t.Errorf("expected 3.14159, got %f", a.Value.AsFloat64())
	}
}

func TestAttrBool(t *testing.T) {
	a := Bool("enabled", true)
	if a.Value.Kind() != KindBool {
		t.Errorf("expected KindBool, got %v", a.Value.Kind())
	}
	if !a.Value.AsBool() {
		t.Error("expected true")
	}
}

func TestAttrDuration(t *testing.T) {
	d := 5 * time.Second
	a := Duration("latency", d)
	if a.Value.Kind() != KindDuration {
		t.Errorf("expected KindDuration, got %v", a.Value.Kind())
	}
	if a.Value.AsDuration() != d {
		t.Errorf("expected %v, got %v", d, a.Value.AsDuration())
	}
}

func TestAttrTime(t *testing.T) {
	now := time.Now()
	a := Time("timestamp", now)
	if a.Value.Kind() != KindTime {
		t.Errorf("expected KindTime, got %v", a.Value.Kind())
	}
	if !a.Value.AsTime().Equal(now) {
		t.Errorf("expected %v, got %v", now, a.Value.AsTime())
	}
}

func TestValueString(t *testing.T) {
	tests := []struct {
		value    Value
		expected string
	}{
		{StringValue("hello"), "hello"},
		{Int64Value(42), "42"},
		{Uint64Value(100), "100"},
		{Float64Value(3.14), "3.14"},
		{BoolValue(true), "true"},
		{BoolValue(false), "false"},
		{DurationValue(time.Second), "1s"},
	}

	for _, tt := range tests {
		got := tt.value.String()
		if got != tt.expected {
			t.Errorf("expected %q, got %q", tt.expected, got)
		}
	}
}

func TestValueAsAny(t *testing.T) {
	v := Int64Value(42)
	if v.AsAny().(int64) != 42 {
		t.Error("AsAny failed for int64")
	}

	v = StringValue("test")
	if v.AsAny().(string) != "test" {
		t.Error("AsAny failed for string")
	}
}
