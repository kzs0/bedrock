package otlp

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/internal/pprof"
)

type boundaryField struct {
	number int
	value  uint64
	data   []byte
}

func boundaryFields(t *testing.T, data []byte) []boundaryField {
	t.Helper()
	var fields []boundaryField
	for len(data) > 0 {
		tag, n := binary.Uvarint(data)
		if n <= 0 {
			t.Fatal("invalid protobuf tag")
		}
		data = data[n:]
		f := boundaryField{number: int(tag >> 3)}
		switch tag & 7 {
		case 0:
			f.value, n = binary.Uvarint(data)
			if n <= 0 {
				t.Fatal("invalid protobuf integer")
			}
			data = data[n:]
		case 1:
			if len(data) < 8 {
				t.Fatal("truncated protobuf fixed64")
			}
			f.value = binary.LittleEndian.Uint64(data)
			data = data[8:]
		case 2:
			size, n := binary.Uvarint(data)
			if n <= 0 || size > uint64(len(data)-n) {
				t.Fatal("invalid protobuf bytes")
			}
			f.data = data[n : n+int(size)]
			data = data[n+int(size):]
		default:
			t.Fatalf("unsupported protobuf wire type %d", tag&7)
		}
		fields = append(fields, f)
	}
	return fields
}

func boundaryAny(t *testing.T, data []byte) any {
	t.Helper()
	fields := boundaryFields(t, data)
	if len(fields) == 0 {
		return nil
	}
	if len(fields) != 1 {
		t.Fatalf("ambiguous AnyValue: %+v", fields)
	}
	f := fields[0]
	switch f.number {
	case 1:
		return string(f.data)
	case 2:
		return f.value != 0
	case 3:
		return int64(f.value)
	case 4:
		return math.Float64frombits(f.value)
	case 5:
		out := []any{}
		for _, item := range boundaryFields(t, f.data) {
			out = append(out, boundaryAny(t, item.data))
		}
		return out
	case 6:
		out := map[string]any{}
		for _, entry := range boundaryFields(t, f.data) {
			kv := boundaryFields(t, entry.data)
			if len(kv) != 2 || kv[0].number != 1 || kv[1].number != 2 {
				t.Fatal("invalid profile key/value entry")
			}
			out[string(kv[0].data)] = boundaryAny(t, kv[1].data)
		}
		return out
	default:
		t.Fatalf("unexpected AnyValue kind %d", f.number)
		return nil
	}
}

func boundaryDictionary() profileDictionary {
	return profileDictionary{strings: []string{""}, stringIndex: map[string]int32{"": 0}, attributes: [][]byte{nil}}
}

func TestProfileTypedAttributesPreserveValues(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  any
	}{
		{"nil", nil, nil}, {"string", "visible", "visible"},
		{"negative", int64(-12), int64(-12)}, {"unsigned", uint64(42), int64(42)},
		{"float", 1.25, 1.25}, {"true", true, true}, {"false", false, false},
		{"array", [3]int{1, 0, -3}, []any{int64(1), int64(0), int64(-3)}},
		{"slice", []any{"marker", nil, false}, []any{"marker", nil, false}},
		{"map", map[string]any{"z": []int{1, 2}, "a": map[string]any{"x": 3.5}}, map[string]any{"z": []any{int64(1), int64(2)}, "a": map[string]any{"x": 3.5}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var b protoBuf
			budget := profileValueBudget{nodes: 4096, bytes: maxProfileBytes}
			if err := appendProfileValue(&b, 2, tc.input, 0, &budget); err != nil {
				t.Fatal(err)
			}
			fields := boundaryFields(t, b.data)
			if len(fields) != 1 || fields[0].number != 2 {
				t.Fatal("AnyValue envelope missing")
			}
			if got := boundaryAny(t, fields[0].data); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("decoded %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestProfileAttributeBudgetsRejectWithoutIndex(t *testing.T) {
	tests := []struct {
		name  string
		value any
		nodes int
		bytes int
	}{
		{"nodes", "text", 0, 1024}, {"envelope bytes", "text", 100, 63},
		{"string bytes", "text", 100, 64}, {"key bytes", map[string]any{"long-key": nil}, 100, 65},
		{"child bytes", map[string]any{"k": "x"}, 100, 65},
		{"unsupported primitive", int32(2), 100, 1024},
		{"unsupported map", map[int]string{1: "x"}, 100, 1024},
		{"map cardinality", func() any {
			m := map[string]int{}
			for i := range 65 {
				m[fmt.Sprint(i)] = i
			}
			return m
		}(), 100, 4096},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var b protoBuf
			budget := profileValueBudget{nodes: tc.nodes, bytes: tc.bytes}
			if err := appendProfileValue(&b, 2, tc.value, 0, &budget); err == nil {
				t.Fatal("invalid attribute accepted")
			}
		})
	}
	e := boundaryDictionary()
	if index := e.attributeUnit(attr.String("large", strings.Repeat("x", maxProfileBytes)), "bytes"); index != 0 || e.err == nil || len(e.attributes) != 1 {
		t.Fatalf("failed attribute acquired an index: %d, %v", index, e.err)
	}
}

func TestProfileNumericLabelClassification(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  bool
	}{
		{nil, false}, {"1", false}, {int64(-1), true}, {uint(3), true}, {1.25, true},
		{[]any{int64(1), float32(2)}, true}, {[2]uint{0, 1}, true},
		{[]any{nil}, false}, {[]any{1, "x"}, false}, {[]any{}, true},
	} {
		if got := numericLabelValue(tc.value); got != tc.want {
			t.Errorf("numeric(%#v)=%v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestProfileLabelUnitsAndRenaming(t *testing.T) {
	sample := &pprof.Sample{
		Label:    map[string][]string{"drop": {"secret"}, "strings": {"first", "second"}, "empty": {}},
		NumLabel: map[string][]int64{"bytes": {0, -7}, "convert": {9}, "mixed": {1, 2}, "plain": {3}},
		NumUnit:  map[string][]string{"bytes": {"bytes", "bytes"}, "convert": {"bytes"}, "mixed": {"ms", "bytes"}},
	}
	e := boundaryDictionary()
	indices := e.sampleAttributes(sample, func(a attr.Attr) (attr.Attr, bool) {
		switch a.Key {
		case "drop":
			return attr.Attr{}, false
		case "convert":
			return attr.String(a.Key, "redacted"), true
		default:
			return a, true
		}
	})
	if e.err != nil || len(indices) != 6 {
		t.Fatalf("labels=%d error=%v", len(indices), e.err)
	}
	values := map[string]any{}
	units := map[string]string{}
	for _, index := range indices {
		fields := boundaryFields(t, e.attributes[index])
		key := e.strings[fields[0].value]
		values[key] = boundaryAny(t, fields[1].data)
		if len(fields) == 3 {
			units[key] = e.strings[fields[2].value]
		}
	}
	want := map[string]any{
		"bytes": []any{int64(0), int64(-7)}, "convert": "redacted", "empty": []any{},
		"mixed": []any{map[string]any{"value": int64(1), "unit": "ms"}, map[string]any{"value": int64(2), "unit": "bytes"}},
		"plain": int64(3), "strings": []any{"first", "second"},
	}
	if !reflect.DeepEqual(values, want) || !reflect.DeepEqual(units, map[string]string{"bytes": "bytes"}) {
		t.Fatalf("labels=%#v units=%#v", values, units)
	}
	merged := boundaryDictionary()
	indices = merged.sampleAttributes(&pprof.Sample{Label: map[string][]string{"a": {"a", "b"}, "b": {"c"}}}, func(a attr.Attr) (attr.Attr, bool) { return attr.Any("merged", a.Value.AsAny()), true })
	if merged.err != nil || len(indices) != 1 {
		t.Fatalf("merge: %v %v", indices, merged.err)
	}
	fields := boundaryFields(t, merged.attributes[indices[0]])
	if got := boundaryAny(t, fields[1].data); !reflect.DeepEqual(got, []any{"a", "b", "c"}) {
		t.Fatalf("merged values: %#v", got)
	}
}

func TestProfileLabelMergeBounds(t *testing.T) {
	for _, arrays := range []bool{false, true} {
		e := boundaryDictionary()
		sample := &pprof.Sample{Label: map[string][]string{}}
		count := 65
		if arrays {
			count = 2
		}
		for i := range count {
			sample.Label[fmt.Sprint(i)] = []string{"x"}
		}
		indices := e.sampleAttributes(sample, func(a attr.Attr) (attr.Attr, bool) { return attr.Any("merged", make([]string, 40)), true })
		if e.err == nil || len(indices) != 0 || len(e.attributes) != 1 {
			t.Fatalf("oversized merge accepted: %v %v", indices, e.err)
		}
	}
}

func TestProfileCompressionAndDurationBoundaries(t *testing.T) {
	for name, raw := range map[string][]byte{"input limit": make([]byte, maxProfileBytes+1), "gzip header": {0x1f, 0x8b}, "gzip truncated": {0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 255}} {
		t.Run(name, func(t *testing.T) {
			if payload, err := EncodeProfileChecked("svc", attr.EmptySet, "heap", raw, time.Now(), time.Now(), ProfileEncodingOptions{}); err == nil || len(payload) != 0 {
				t.Fatal("invalid compressed input exported")
			}
		})
	}
	var compressed bytes.Buffer
	w := gzip.NewWriter(&compressed)
	if _, err := w.Write(make([]byte, maxProfileBytes+1)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := EncodeProfileChecked("svc", attr.EmptySet, "heap", compressed.Bytes(), time.Now(), time.Now(), ProfileEncodingOptions{}); err == nil || !strings.Contains(err.Error(), "expanded profile") {
		t.Fatalf("expansion limit: %v", err)
	}
	raw := profileWireFixture(profileWireVarint(2, 0), profileWireVarint(1, 1), profileWireVarint(1, 1), profileWireVarint(1, 1))
	start := time.Unix(2, 0)
	payload, err := EncodeProfileChecked("svc", attr.EmptySet, "heap", raw, start, start.Add(-time.Second), ProfileEncodingOptions{})
	if err != nil {
		t.Fatal(err)
	}
	resource := boundaryFields(t, boundaryFields(t, payload)[0].data)
	scope := boundaryFields(t, resource[1].data)
	profile := boundaryFields(t, scope[0].data)
	for _, f := range profile {
		if f.number == 4 && f.value != 0 {
			t.Fatalf("negative duration exported: %d", f.value)
		}
	}
}

func TestProfileEncodingPreservesNativeSampleDimensions(t *testing.T) {
	v, b, join := profileWireVarint, profileWireBytes, profileWireJoin
	negative := int64(-7)
	raw := profileWireFixture(join(v(1, 3), v(2, uint64(negative)), v(2, 0)), join(v(1, 9), v(2, 0x1000), v(3, 0x2000), v(4, 16), v(5, 3)), join(v(1, 3), v(2, 9), v(3, 0x1234), b(4, join(v(1, 11), v(2, 7)))), join(v(1, 11), v(2, 4), v(3, 5), v(4, 6), v(5, 42)))
	for _, text := range []string{"/private/bin/program", "visibleFunction", "systemFunction", "/private/source/work.go"} {
		raw = append(raw, b(6, []byte(text))...)
	}
	raw = append(raw, b(1, join(v(1, 1), v(2, 2)))...)
	raw = append(raw, b(11, join(v(1, 1), v(2, 2)))...)
	raw = append(raw, v(12, 99)...)
	start := time.Unix(1, 0)
	for _, paths := range []bool{false, true} {
		payload, err := EncodeProfileChecked("svc", attr.EmptySet, "cpu", raw, start, start.Add(time.Second), ProfileEncodingOptions{SourcePaths: paths})
		if err != nil {
			t.Fatal(err)
		}
		outer := boundaryFields(t, payload)
		var dictionaryStrings []string
		for _, f := range boundaryFields(t, outer[1].data) {
			if f.number == 5 {
				dictionaryStrings = append(dictionaryStrings, string(f.data))
			}
		}
		contains := func(s string) bool {
			for _, text := range dictionaryStrings {
				if text == s {
					return true
				}
			}
			return false
		}
		if paths {
			if !contains("/private/source/work.go") || !contains("/private/bin/program") {
				t.Fatal("source opt-in did not retain full paths")
			}
		} else if contains("/private/source/work.go") || contains("/private/bin/program") || !contains("work.go") || !contains("program") {
			t.Fatal("default source path sanitization failed")
		}
		resource := boundaryFields(t, outer[0].data)
		profiles := boundaryFields(t, resource[1].data)
		if len(profiles) != 2 {
			t.Fatalf("sample dimensions=%d", len(profiles))
		}
		for i, profile := range profiles {
			var value int64
			var id []byte
			var sampleCount int
			var sawStart, sawDuration, sawPeriod bool
			for _, field := range boundaryFields(t, profile.data) {
				switch field.number {
				case 2:
					sampleCount++
					for _, sampleField := range boundaryFields(t, field.data) {
						if sampleField.number == 1 && sampleField.value == 0 {
							t.Fatal("sample stack lost")
						}
						if sampleField.number == 2 {
							value = int64(sampleField.value)
						}
					}
				case 3:
					sawStart = true
					if field.value != uint64(start.UnixNano()) {
						t.Fatal("profile start changed")
					}
				case 4:
					sawDuration = true
					if field.value != uint64(time.Second) {
						t.Fatal("profile duration changed")
					}
				case 6:
					sawPeriod = true
					if field.value != 99 {
						t.Fatal("sampling period changed")
					}
				case 7:
					id = field.data
				}
			}
			want := []int64{-7, 0}[i]
			if sampleCount != 1 || value != want || len(id) != 16 || !sawStart || !sawDuration || !sawPeriod {
				t.Fatalf("dimension %d: count=%d value=%d id=%x", i, sampleCount, value, id)
			}
		}
	}
}
