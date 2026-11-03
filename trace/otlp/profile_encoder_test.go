package otlp

import (
	"bytes"
	"runtime/pprof"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
)

func TestEncodeProfile_NonEmpty(t *testing.T) {
	resource := attr.NewSet(attr.String("env", "test"), attr.String("region", "us-east-1"))
	now := time.Now()
	payload := EncodeProfile("my-service", resource, "heap", profileFixture(t), now, now.Add(time.Second))
	if len(payload) == 0 {
		t.Fatal("expected non-empty encoded profile")
	}
}

func TestEncodeProfile_EmptyPprof(t *testing.T) {
	resource := attr.EmptySet
	now := time.Now()
	// Empty profiles cannot produce valid exports.
	payload := EncodeProfile("svc", resource, "goroutine", nil, now, now)
	if len(payload) != 0 {
		t.Fatal("invalid profile produced an export")
	}
}

func TestEncodeProfile_Determinism(t *testing.T) {
	// Two calls with the same inputs should produce similar-sized output.
	// (profile_id is random, so exact equality won't hold.)
	resource := attr.NewSet(attr.String("svc", "test"))
	now := time.Now()
	fixture := profileFixture(t)
	p1 := EncodeProfile("svc", resource, "cpu", fixture, now, now)
	p2 := EncodeProfile("svc", resource, "cpu", fixture, now, now)
	// Both should be non-empty and close in size (differ only in 16-byte random ID).
	if len(p1) == 0 || len(p2) == 0 {
		t.Fatal("expected non-empty output")
	}
	diff := len(p1) - len(p2)
	if diff < 0 {
		diff = -diff
	}
	if diff > 5 {
		t.Errorf("outputs differ by more than expected (random ID only): %d vs %d bytes", len(p1), len(p2))
	}
}

func profileFixture(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := pprof.Lookup("heap").WriteTo(&b, 0); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
