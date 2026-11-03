package pprof

import (
	"encoding/binary"
	"testing"
)

func TestParserWireAndBudgetBoundaries(t *testing.T) {
	for name, raw := range map[string][]byte{
		"input size":    make([]byte, maxBytes+1),
		"short fixed64": {9, 1, 2}, "short fixed32": {13, 1, 2},
		"string wrong wire":          testVarint(6, 1),
		"string invalid UTF8":        testMessage(6, []byte{0xff}),
		"sample scalar wrong wire":   testMessage(1, testMessage(1, nil)),
		"sample repeated wrong wire": testMessage(2, []byte{13, 0, 0, 0, 0}),
		"sample packed truncated":    testMessage(2, testMessage(1, []byte{0x80})),
	} {
		t.Run(name, func(t *testing.T) {
			if profile, err := ParseUncompressed(raw); err == nil || profile != nil {
				t.Fatal("malformed wire produced a profile")
			}
		})
	}
	for _, d := range []decoder{{items: maxItems}, {memory: 128 << 20}} {
		if err := d.reserve(1); err == nil {
			t.Fatal("decoder budget exceeded")
		}
	}
	valid := decoder{items: maxItems - 1, memory: (128 << 20) - 1}
	if err := valid.reserve(1); err != nil {
		t.Fatalf("exact budget rejected: %v", err)
	}
	if valid.items != maxItems || valid.memory != 128<<20 {
		t.Fatal("budget not charged")
	}
	for _, packed := range []bool{false, true} {
		d := decoder{items: maxItems}
		f := field{wire: 0, value: 7}
		if packed {
			f = field{wire: 2, data: binary.AppendUvarint(nil, 7)}
		}
		var out []uint64
		if err := d.repeated(f, &out); err == nil || len(out) != 0 {
			t.Fatal("repeated field exceeded budget")
		}
	}
	d := decoder{items: maxItems}
	if err := d.fields(testVarint(1, 1), func(field) error { t.Error("over-budget field visited"); return nil }); err == nil {
		t.Fatal("field bypassed item budget")
	}
}

func TestProfileResolvedDimensionValidation(t *testing.T) {
	for _, p := range []*Profile{nil, {}, {SampleType: []*ValueType{{}}, Sample: []*Sample{nil}}, {SampleType: []*ValueType{{}}, Sample: []*Sample{{Value: []int64{1, 2}}}}} {
		if p.CheckValid() == nil {
			t.Fatal("invalid resolved dimensions accepted")
		}
	}
	if err := (&Profile{SampleType: []*ValueType{{Type: "samples", Unit: "count"}}, Sample: []*Sample{{Value: []int64{0}}}}).CheckValid(); err != nil {
		t.Fatalf("zero sample rejected: %v", err)
	}
}
