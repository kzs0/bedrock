package attr

import (
	"sort"
)

// Set is an immutable collection of attributes, sorted by key.
// Duplicate keys are deduplicated (last value wins).
type Set struct {
	attrs []Attr
}

// NewSet creates a new Set from the given attributes.
// Attributes are sorted by key, and duplicates are deduplicated.
func NewSet(attrs ...Attr) Set {
	if len(attrs) == 0 {
		return Set{}
	}

	// Make a copy to avoid modifying the input
	sorted := make([]Attr, len(attrs))
	copy(sorted, attrs)

	// Sort by key
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Key < sorted[j].Key
	})

	// Deduplicate (keep last value for each key)
	deduped := sorted[:0]
	for i, a := range sorted {
		if i > 0 && sorted[i-1].Key == a.Key {
			deduped[len(deduped)-1] = a
		} else {
			deduped = append(deduped, a)
		}
	}

	return Set{attrs: deduped}
}

// Len returns the number of attributes in the set.
func (s Set) Len() int {
	return len(s.attrs)
}

// Attrs returns a slice of the attributes.
// The returned slice should not be modified.
func (s Set) Attrs() []Attr {
	return s.attrs
}

// Get returns the value for the given key, or zero Value if not found.
func (s Set) Get(key string) (Value, bool) {
	i := sort.Search(len(s.attrs), func(i int) bool {
		return s.attrs[i].Key >= key
	})
	if i < len(s.attrs) && s.attrs[i].Key == key {
		return s.attrs[i].Value, true
	}
	return Value{}, false
}

// Has returns true if the set contains the given key.
func (s Set) Has(key string) bool {
	_, ok := s.Get(key)
	return ok
}

// Merge creates a new Set by merging this set with additional attributes.
// Attributes in 'other' override those in this set if keys match.
func (s Set) Merge(other ...Attr) Set {
	if len(other) == 0 {
		return s
	}
	if len(s.attrs) == 0 {
		return NewSet(other...)
	}

	combined := make([]Attr, 0, len(s.attrs)+len(other))
	combined = append(combined, s.attrs...)
	combined = append(combined, other...)
	return NewSet(combined...)
}

// MergeSet creates a new Set by merging this set with another set.
// Attributes in 'other' override those in this set if keys match.
func (s Set) MergeSet(other Set) Set {
	if other.Len() == 0 {
		return s
	}
	if s.Len() == 0 {
		return other
	}

	combined := make([]Attr, 0, len(s.attrs)+len(other.attrs))
	combined = append(combined, s.attrs...)
	combined = append(combined, other.attrs...)
	return NewSet(combined...)
}

// Range iterates over all attributes in the set.
func (s Set) Range(fn func(Attr) bool) {
	for _, a := range s.attrs {
		if !fn(a) {
			return
		}
	}
}

// Keys returns a slice of all keys in the set.
func (s Set) Keys() []string {
	keys := make([]string, len(s.attrs))
	for i, a := range s.attrs {
		keys[i] = a.Key
	}
	return keys
}

// EmptySet is an empty attribute set.
var EmptySet = Set{}
