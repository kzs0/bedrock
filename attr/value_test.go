package attr

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestUint64(t *testing.T) {
	a := Uint64("count", 42)
	if a.Key != "count" {
		t.Errorf("expected key 'count', got %q", a.Key)
	}
	if a.Value.Kind() != KindUint64 {
		t.Errorf("expected KindUint64, got %v", a.Value.Kind())
	}
	if a.Value.AsUint64() != 42 {
		t.Errorf("expected 42, got %d", a.Value.AsUint64())
	}
}

func TestAny_String(t *testing.T) {
	a := Any("key", "value")
	if a.Value.Kind() != KindString {
		t.Errorf("expected KindString for string input, got %v", a.Value.Kind())
	}
	if a.Value.AsString() != "value" {
		t.Errorf("expected 'value', got %q", a.Value.AsString())
	}
}

func TestAny_Int(t *testing.T) {
	a := Any("key", 42)
	if a.Value.Kind() != KindInt64 {
		t.Errorf("expected KindInt64, got %v", a.Value.Kind())
	}
	if a.Value.AsInt64() != 42 {
		t.Errorf("expected 42, got %d", a.Value.AsInt64())
	}
}

func TestAny_Int64(t *testing.T) {
	a := Any("key", int64(100))
	if a.Value.Kind() != KindInt64 {
		t.Errorf("expected KindInt64, got %v", a.Value.Kind())
	}
}

func TestAny_Uint64(t *testing.T) {
	a := Any("key", uint64(100))
	if a.Value.Kind() != KindUint64 {
		t.Errorf("expected KindUint64, got %v", a.Value.Kind())
	}
}

func TestAny_Float64(t *testing.T) {
	a := Any("key", 3.14)
	if a.Value.Kind() != KindFloat64 {
		t.Errorf("expected KindFloat64, got %v", a.Value.Kind())
	}
}

func TestAny_Bool(t *testing.T) {
	a := Any("key", true)
	if a.Value.Kind() != KindBool {
		t.Errorf("expected KindBool, got %v", a.Value.Kind())
	}
}

func TestAny_Duration(t *testing.T) {
	a := Any("key", 5*time.Second)
	if a.Value.Kind() != KindDuration {
		t.Errorf("expected KindDuration, got %v", a.Value.Kind())
	}
}

func TestAny_Time(t *testing.T) {
	now := time.Now()
	a := Any("key", now)
	if a.Value.Kind() != KindTime {
		t.Errorf("expected KindTime, got %v", a.Value.Kind())
	}
}

func TestAny_Fallback(t *testing.T) {
	type custom struct{ X int }
	a := Any("key", custom{X: 1})
	if a.Value.Kind() != KindAny {
		t.Errorf("expected KindAny for custom type, got %v", a.Value.Kind())
	}
}

func TestNewEvent(t *testing.T) {
	e := NewEvent("cache.hit", String("key", "user:123"), Int("size", 100))
	if e.Name != "cache.hit" {
		t.Errorf("expected name 'cache.hit', got %q", e.Name)
	}
	if len(e.Attrs) != 2 {
		t.Errorf("expected 2 attrs, got %d", len(e.Attrs))
	}
}

func TestAttr_StringMethod(t *testing.T) {
	a := String("key", "value")
	s := a.String()
	if s != "key=value" {
		t.Errorf("expected 'key=value', got %q", s)
	}
}

func TestWithKey(t *testing.T) {
	a := String("original", "value")
	b := a.WithKey("renamed")
	if b.Key != "renamed" {
		t.Errorf("expected key 'renamed', got %q", b.Key)
	}
	if b.Value.AsString() != "value" {
		t.Errorf("expected value 'value', got %q", b.Value.AsString())
	}
	// Original should be unchanged
	if a.Key != "original" {
		t.Error("original attr key should be unchanged")
	}
}

func TestAsUint64(t *testing.T) {
	v := Uint64Value(12345)
	if v.AsUint64() != 12345 {
		t.Errorf("expected 12345, got %d", v.AsUint64())
	}
}

func TestAsUint64_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for wrong kind")
		}
	}()
	v := StringValue("hello")
	_ = v.AsUint64()
}

func TestAsString_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for wrong kind")
		}
	}()
	v := Int64Value(42)
	_ = v.AsString()
}

func TestAsInt64_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for wrong kind")
		}
	}()
	v := StringValue("hello")
	_ = v.AsInt64()
}

func TestAsFloat64_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for wrong kind")
		}
	}()
	v := StringValue("hello")
	_ = v.AsFloat64()
}

func TestAsBool_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for wrong kind")
		}
	}()
	v := StringValue("hello")
	_ = v.AsBool()
}

func TestAsDuration_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for wrong kind")
		}
	}()
	v := StringValue("hello")
	_ = v.AsDuration()
}

func TestAsTime_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for wrong kind")
		}
	}()
	v := StringValue("hello")
	_ = v.AsTime()
}

func TestErrorDetail_Err(t *testing.T) {
	testErr := errors.New("test error")
	a := Error(testErr)
	det := a.Value.AsError()
	if det == nil {
		t.Fatal("expected error detail")
	}
	if det.Err() != testErr {
		t.Errorf("expected %v, got %v", testErr, det.Err())
	}
}

func TestAsError_NonError(t *testing.T) {
	v := StringValue("hello")
	det := v.AsError()
	if det != nil {
		t.Error("expected nil for non-error value")
	}
}

func TestAsAny_AllKinds(t *testing.T) {
	tests := []struct {
		name string
		v    Value
	}{
		{"string", StringValue("hello")},
		{"int64", Int64Value(42)},
		{"uint64", Uint64Value(100)},
		{"float64", Float64Value(3.14)},
		{"bool", BoolValue(true)},
		{"duration", DurationValue(5 * time.Second)},
		{"time", TimeValue(time.Now())},
		{"error", ErrorValue(errors.New("err"), 0)},
		{"error_nil_detail", Value{kind: KindError}},
		{"any", Value{kind: KindAny, any: struct{}{}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Should not panic
			result := tt.v.AsAny()
			_ = result
		})
	}
}

func TestValue_String_AllKinds(t *testing.T) {
	tests := []struct {
		name string
		v    Value
		want string
	}{
		{"string", StringValue("hello"), "hello"},
		{"int64", Int64Value(42), "42"},
		{"uint64", Uint64Value(100), "100"},
		{"float64", Float64Value(3.14), "3.14"},
		{"bool_true", BoolValue(true), "true"},
		{"bool_false", BoolValue(false), "false"},
		{"duration", DurationValue(5 * time.Second), "5s"},
		{"error", ErrorValue(errors.New("oops"), 0), "oops"},
		{"error_nil_detail", Value{kind: KindError}, ""},
		{"any", Value{kind: KindAny, any: 42}, "42"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.v.String()
			if got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}

	// Time value - just verify it doesn't panic
	tv := TimeValue(time.Now())
	s := tv.String()
	if s == "" {
		t.Error("time String() should not be empty")
	}
}

func TestErrorWithStack(t *testing.T) {
	// nil error
	a := ErrorWithStack(nil, 0)
	if a.Value.Kind() != KindString {
		t.Errorf("nil error should produce string value, got %v", a.Value.Kind())
	}

	// real error
	testErr := errors.New("test")
	a = ErrorWithStack(testErr, 0)
	if a.Value.Kind() != KindError {
		t.Errorf("expected KindError, got %v", a.Value.Kind())
	}
	det := a.Value.AsError()
	if det == nil || det.Err() != testErr {
		t.Error("expected error detail with original error")
	}
}

func TestErrorDetail_Fingerprint(t *testing.T) {
	testErr := errors.New("test")
	a := Error(testErr)
	det := a.Value.AsError()
	fp := det.Fingerprint()
	if len(fp) != 16 {
		t.Errorf("expected 16-char fingerprint, got %d chars: %q", len(fp), fp)
	}

	// Same error at same call site should produce same fingerprint
	a2 := Error(testErr)
	det2 := a2.Value.AsError()
	fp2 := det2.Fingerprint()
	if fp != fp2 {
		t.Errorf("fingerprints should match for same call site: %q vs %q", fp, fp2)
	}
}

func TestErrorDetail_FormatChain(t *testing.T) {
	inner := errors.New("inner")
	outer := fmt.Errorf("outer: %w", inner)
	a := Error(outer)
	det := a.Value.AsError()
	chain := det.FormatChain()
	if chain == "" {
		t.Error("expected non-empty chain for wrapped error")
	}

	// Non-wrapped error should return empty chain
	a2 := Error(errors.New("simple"))
	det2 := a2.Value.AsError()
	chain2 := det2.FormatChain()
	if chain2 != "" {
		t.Errorf("expected empty chain for non-wrapped error, got %q", chain2)
	}
}

func TestErrorDetail_FormatStack(t *testing.T) {
	a := Error(errors.New("test"))
	det := a.Value.AsError()
	stack := det.FormatStack()
	if stack == "" {
		t.Error("expected non-empty stack trace")
	}
}

func TestSetAttrs(t *testing.T) {
	s := NewSet(
		String("a", "1"),
		Int("b", 2),
		Bool("c", true),
	)
	attrs := s.Attrs()
	if len(attrs) != 3 {
		t.Errorf("expected 3 attrs, got %d", len(attrs))
	}
}
