package attr

import (
	"testing"
)

func TestSetBasic(t *testing.T) {
	s := NewSet(
		String("b", "2"),
		String("a", "1"),
		String("c", "3"),
	)

	if s.Len() != 3 {
		t.Errorf("expected 3 attrs, got %d", s.Len())
	}

	// Should be sorted by key
	keys := s.Keys()
	expected := []string{"a", "b", "c"}
	for i, k := range keys {
		if k != expected[i] {
			t.Errorf("expected key %q at position %d, got %q", expected[i], i, k)
		}
	}
}

func TestSetDeduplication(t *testing.T) {
	s := NewSet(
		String("key", "first"),
		String("key", "second"),
		String("key", "third"),
	)

	if s.Len() != 1 {
		t.Errorf("expected 1 attr after dedup, got %d", s.Len())
	}

	v, ok := s.Get("key")
	if !ok {
		t.Fatal("key not found")
	}
	if v.AsString() != "third" {
		t.Errorf("expected 'third' (last value wins), got %q", v.AsString())
	}
}

func TestSetGet(t *testing.T) {
	s := NewSet(
		String("a", "1"),
		Int("b", 2),
	)

	v, ok := s.Get("a")
	if !ok {
		t.Error("key 'a' not found")
	}
	if v.AsString() != "1" {
		t.Errorf("expected '1', got %q", v.AsString())
	}

	v, ok = s.Get("b")
	if !ok {
		t.Error("key 'b' not found")
	}
	if v.AsInt64() != 2 {
		t.Errorf("expected 2, got %d", v.AsInt64())
	}

	_, ok = s.Get("missing")
	if ok {
		t.Error("expected missing key to not be found")
	}
}

func TestSetHas(t *testing.T) {
	s := NewSet(String("exists", "yes"))

	if !s.Has("exists") {
		t.Error("expected key to exist")
	}
	if s.Has("missing") {
		t.Error("expected key to not exist")
	}
}

func TestSetMerge(t *testing.T) {
	s1 := NewSet(
		String("a", "1"),
		String("b", "2"),
	)

	s2 := s1.Merge(
		String("b", "override"),
		String("c", "3"),
	)

	if s2.Len() != 3 {
		t.Errorf("expected 3 attrs, got %d", s2.Len())
	}

	v, _ := s2.Get("b")
	if v.AsString() != "override" {
		t.Errorf("expected 'override', got %q", v.AsString())
	}

	// Original should be unchanged
	v, _ = s1.Get("b")
	if v.AsString() != "2" {
		t.Errorf("original should be unchanged, got %q", v.AsString())
	}
}

func TestSetMergeSet(t *testing.T) {
	s1 := NewSet(String("a", "1"))
	s2 := NewSet(String("b", "2"))

	merged := s1.MergeSet(s2)

	if merged.Len() != 2 {
		t.Errorf("expected 2 attrs, got %d", merged.Len())
	}
	if !merged.Has("a") || !merged.Has("b") {
		t.Error("merged set missing keys")
	}
}

func TestSetRange(t *testing.T) {
	s := NewSet(
		String("a", "1"),
		String("b", "2"),
		String("c", "3"),
	)

	var count int
	s.Range(func(a Attr) bool {
		count++
		return true
	})

	if count != 3 {
		t.Errorf("expected to iterate 3 times, got %d", count)
	}

	// Test early termination
	count = 0
	s.Range(func(a Attr) bool {
		count++
		return false // Stop after first
	})

	if count != 1 {
		t.Errorf("expected to iterate 1 time with early stop, got %d", count)
	}
}

func TestEmptySet(t *testing.T) {
	s := NewSet()

	if s.Len() != 0 {
		t.Errorf("expected 0 attrs, got %d", s.Len())
	}

	_, ok := s.Get("any")
	if ok {
		t.Error("expected empty set to not have any keys")
	}
}
