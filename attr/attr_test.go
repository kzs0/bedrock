package attr

import (
	"errors"
	"fmt"
	"strings"
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

// ── Error attribute tests ─────────────────────────────────────────────────────

func TestAttrError_NilIsString(t *testing.T) {
	a := Error(nil)
	if a.Key != "error" {
		t.Fatalf("expected key 'error', got %q", a.Key)
	}
	if a.Value.Kind() != KindString {
		t.Fatalf("expected KindString for nil error, got %v", a.Value.Kind())
	}
	if a.Value.AsString() != "" {
		t.Fatalf("expected empty string for nil error, got %q", a.Value.AsString())
	}
}

func TestAttrError_Kind(t *testing.T) {
	err := errors.New("boom")
	a := Error(err)
	if a.Key != "error" {
		t.Fatalf("expected key 'error', got %q", a.Key)
	}
	if a.Value.Kind() != KindError {
		t.Fatalf("expected KindError, got %v", a.Value.Kind())
	}
}

func TestAttrError_StringReturnsMessage(t *testing.T) {
	err := errors.New("something failed")
	a := Error(err)
	if a.Value.String() != "something failed" {
		t.Errorf("expected error message as string, got %q", a.Value.String())
	}
}

func TestAttrError_TypeName(t *testing.T) {
	err := errors.New("any error")
	a := Error(err)
	det := a.Value.AsError()
	if det == nil {
		t.Fatal("AsError returned nil")
	}
	if det.TypeName() == "" {
		t.Error("TypeName should not be empty")
	}
}

func TestAttrError_StackNotEmpty(t *testing.T) {
	err := errors.New("stack test")
	a := Error(err)
	det := a.Value.AsError()
	if det == nil {
		t.Fatal("AsError returned nil")
	}
	stack := det.FormatStack()
	if stack == "" {
		t.Error("expected non-empty stack trace")
	}
	// Stack should contain the test function name
	if !strings.Contains(stack, "TestAttrError_StackNotEmpty") {
		t.Errorf("stack trace should contain calling function, got:\n%s", stack)
	}
}

func TestAttrErrorWithStack_SkipFrames(t *testing.T) {
	err := errors.New("skip test")
	// skip=0 should include the current frame
	a0 := ErrorWithStack(err, 0)
	// skip=1 should skip this frame
	a1 := ErrorWithStack(err, 1)

	stack0 := a0.Value.AsError().FormatStack()
	stack1 := a1.Value.AsError().FormatStack()

	// stack0 should contain this test function; stack1 may not
	if !strings.Contains(stack0, "TestAttrErrorWithStack_SkipFrames") {
		t.Errorf("skip=0 should include calling frame, got:\n%s", stack0)
	}
	_ = stack1 // just verify no panic
}

func TestAttrError_Chain(t *testing.T) {
	inner := errors.New("inner error")
	outer := fmt.Errorf("outer: %w", inner)
	a := Error(outer)
	det := a.Value.AsError()
	if det == nil {
		t.Fatal("AsError returned nil")
	}
	chain := det.FormatChain()
	if chain == "" {
		t.Error("expected non-empty error chain for wrapped error")
	}
	if !strings.Contains(chain, "inner error") {
		t.Errorf("chain should include inner error message, got: %s", chain)
	}
}

func TestAttrError_ChainNoWrapping(t *testing.T) {
	err := errors.New("standalone")
	a := Error(err)
	det := a.Value.AsError()
	chain := det.FormatChain()
	if chain != "" {
		t.Errorf("unwrapped error should have empty chain, got: %s", chain)
	}
}

func TestAttrError_FingerprintConsistency(t *testing.T) {
	// Two calls at the same call site should produce the same fingerprint.
	err := errors.New("consistent")
	a1 := Error(err)
	a2 := Error(err)
	fp1 := a1.Value.AsError().Fingerprint()
	fp2 := a2.Value.AsError().Fingerprint()
	if fp1 != fp2 {
		t.Errorf("fingerprints should be equal for same call site: %s vs %s", fp1, fp2)
	}
	if fp1 == "" {
		t.Error("fingerprint should not be empty")
	}
}

func TestAttrError_FingerprintDifferentCallSites(t *testing.T) {
	err := errors.New("diff")
	fp1 := callSite1(err)
	fp2 := callSite2(err)
	if fp1 == fp2 {
		t.Errorf("fingerprints from different call sites should differ: both=%s", fp1)
	}
}

func callSite1(err error) string { return Error(err).Value.AsError().Fingerprint() }
func callSite2(err error) string { return Error(err).Value.AsError().Fingerprint() }

func TestAttrError_AsAnyReturnsString(t *testing.T) {
	err := errors.New("as any")
	a := Error(err)
	got := a.Value.AsAny()
	s, ok := got.(string)
	if !ok {
		t.Fatalf("AsAny should return string for KindError, got %T", got)
	}
	if s != "as any" {
		t.Errorf("expected %q, got %q", "as any", s)
	}
}
