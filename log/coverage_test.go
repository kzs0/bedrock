package log

import (
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
)

func TestAttrToSlog_Uint64(t *testing.T) {
	a := AttrToSlog(attr.Uint64("count", 42))
	if a.Key != "count" {
		t.Errorf("expected key 'count', got %q", a.Key)
	}
	if a.Value.Uint64() != 42 {
		t.Errorf("expected 42, got %d", a.Value.Uint64())
	}
}

func TestAttrToSlog_Time(t *testing.T) {
	now := time.Now()
	a := AttrToSlog(attr.Time("ts", now))
	if a.Key != "ts" {
		t.Errorf("expected key 'ts', got %q", a.Key)
	}
	if a.Value.Time().IsZero() {
		t.Error("expected non-zero time")
	}
}

func TestAttrToSlog_AnyFallback(t *testing.T) {
	type custom struct{ X int }
	a := AttrToSlog(attr.Any("data", custom{X: 1}))
	if a.Key != "data" {
		t.Errorf("expected key 'data', got %q", a.Key)
	}
}
