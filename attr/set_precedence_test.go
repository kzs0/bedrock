package attr

import (
	"fmt"
	"testing"
)

func TestSetPreservesLastValueAcrossLargeResourceSets(t *testing.T) {
	for size := 16; size <= 128; size++ {
		attrs := []Attr{String("host.name", "detected-host")}
		for i := range size {
			attrs = append(attrs, String(fmt.Sprintf("resource.%03d", size-i), "detected"))
		}
		attrs = append(attrs, String("host.name", "explicit-host"))
		base := NewSet(attrs[:len(attrs)-1]...)
		for name, set := range map[string]Set{
			"new":       NewSet(attrs...),
			"merge":     base.Merge(attrs[len(attrs)-1]),
			"merge_set": base.MergeSet(NewSet(attrs[len(attrs)-1])),
		} {
			if value, ok := set.Get("host.name"); !ok || value.AsString() != "explicit-host" {
				t.Fatalf("%s, %d resource attributes: explicit override lost: %v", name, size, value)
			}
			if set.Len() != size+1 || attrs[0].Value.AsString() != "detected-host" || attrs[len(attrs)-1].Value.AsString() != "explicit-host" {
				t.Fatalf("%s, %d resource attributes: count or caller input changed", name, size)
			}
		}
		if value, _ := base.Get("host.name"); value.AsString() != "detected-host" {
			t.Fatalf("%d resource attributes: immutable base changed", size)
		}
	}
}
