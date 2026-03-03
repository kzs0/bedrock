package internal

import (
	"testing"
)

func TestNewTraceID(t *testing.T) {
	id := NewTraceID()
	if id.IsZero() {
		t.Error("NewTraceID should not return zero ID")
	}

	// Two IDs should be different
	id2 := NewTraceID()
	if id == id2 {
		t.Error("NewTraceID should return unique IDs")
	}
}

func TestNewSpanID(t *testing.T) {
	id := NewSpanID()
	if id.IsZero() {
		t.Error("NewSpanID should not return zero ID")
	}

	id2 := NewSpanID()
	if id == id2 {
		t.Error("NewSpanID should return unique IDs")
	}
}

func TestTraceID_String(t *testing.T) {
	id := NewTraceID()
	s := id.String()
	if len(s) != 32 {
		t.Errorf("expected 32-char hex string, got %d chars: %q", len(s), s)
	}
	// Verify it's valid hex
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Errorf("invalid hex character %c in trace ID string", c)
		}
	}
}

func TestSpanID_String(t *testing.T) {
	id := NewSpanID()
	s := id.String()
	if len(s) != 16 {
		t.Errorf("expected 16-char hex string, got %d chars: %q", len(s), s)
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Errorf("invalid hex character %c in span ID string", c)
		}
	}
}

func TestTraceID_IsZero(t *testing.T) {
	var zero TraceID
	if !zero.IsZero() {
		t.Error("zero TraceID should be zero")
	}

	nonZero := NewTraceID()
	if nonZero.IsZero() {
		t.Error("NewTraceID should not be zero")
	}
}

func TestSpanID_IsZero(t *testing.T) {
	var zero SpanID
	if !zero.IsZero() {
		t.Error("zero SpanID should be zero")
	}

	nonZero := NewSpanID()
	if nonZero.IsZero() {
		t.Error("NewSpanID should not be zero")
	}
}

func TestTraceIDFromHex(t *testing.T) {
	original := NewTraceID()
	hex := original.String()

	parsed, err := TraceIDFromHex(hex)
	if err != nil {
		t.Fatalf("TraceIDFromHex(%q) error: %v", hex, err)
	}
	if parsed != original {
		t.Errorf("round-trip failed: got %s, want %s", parsed.String(), original.String())
	}
}

func TestTraceIDFromHex_Invalid(t *testing.T) {
	_, err := TraceIDFromHex("not-valid-hex")
	if err == nil {
		t.Error("expected error for invalid hex")
	}
}

func TestSpanIDFromHex(t *testing.T) {
	original := NewSpanID()
	hex := original.String()

	parsed, err := SpanIDFromHex(hex)
	if err != nil {
		t.Fatalf("SpanIDFromHex(%q) error: %v", hex, err)
	}
	if parsed != original {
		t.Errorf("round-trip failed: got %s, want %s", parsed.String(), original.String())
	}
}

func TestSpanIDFromHex_Invalid(t *testing.T) {
	_, err := SpanIDFromHex("zzzz")
	if err == nil {
		t.Error("expected error for invalid hex")
	}
}
