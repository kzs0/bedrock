package otlp

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/internal"
	"github.com/kzs0/bedrock/internal/pprof"
)

const maxProfileBytes = 32 << 20

// ProfileEncodingOptions controls exported profile details.
type ProfileEncodingOptions struct {
	SourcePaths bool
	LabelFilter func(attr.Attr) (attr.Attr, bool)
}

// EncodeProfile converts pprof into native OTLP v0.2.0.
// Invalid profiles return nil.
func EncodeProfile(service string, resource attr.Set, profileType string, raw []byte, start, end time.Time) []byte {
	data, _ := EncodeProfileChecked(service, resource, profileType, raw, start, end, ProfileEncodingOptions{})
	return data
}

// EncodeProfileChecked validates and converts bounded profiles.
// Attributes permit 64 items, 4096 nodes, depth eight.
// Exceeding encoding bounds returns an error.
func EncodeProfileChecked(service string, resource attr.Set, profileType string, raw []byte, start, end time.Time, options ProfileEncodingOptions) ([]byte, error) {
	if len(raw) > maxProfileBytes {
		return nil, fmt.Errorf("otlp/profiles: profile exceeds limit")
	}
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		reader, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		expanded, err := io.ReadAll(io.LimitReader(reader, maxProfileBytes+1))
		_ = reader.Close()
		if err != nil {
			return nil, err
		}
		if len(expanded) > maxProfileBytes {
			return nil, fmt.Errorf("otlp/profiles: expanded profile exceeds limit")
		}
		raw = expanded
	}
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		return nil, fmt.Errorf("otlp/profiles: nested gzip is unsupported")
	}
	parsed, err := pprof.ParseUncompressed(raw)
	if err != nil {
		return nil, fmt.Errorf("otlp/profiles: invalid pprof: %w", err)
	}
	if err = parsed.CheckValid(); err != nil {
		return nil, err
	}
	if len(parsed.SampleType) == 0 {
		return nil, fmt.Errorf("otlp/profiles: missing sample types")
	}
	enc := profileDictionary{strings: []string{""}, stringIndex: map[string]int32{"": 0}, options: options}
	enc.mapping = append(enc.mapping, nil)
	enc.locations = append(enc.locations, nil)
	enc.functions = append(enc.functions, nil)
	enc.stacks = append(enc.stacks, nil)
	enc.attributes = append(enc.attributes, nil)
	functions := map[*pprof.Function]int32{}
	for _, fn := range parsed.Function {
		var b protoBuf
		b.appendVarintField(1, uint64(enc.str(fn.Name)))
		b.appendVarintField(2, uint64(enc.str(fn.SystemName)))
		b.appendVarintField(3, uint64(enc.str(enc.path(fn.Filename))))
		b.appendVarintField(4, uint64(fn.StartLine))
		functions[fn] = int32(len(enc.functions))
		enc.functions = append(enc.functions, b.data)
	}
	mappings := map[*pprof.Mapping]int32{}
	for _, mapping := range parsed.Mapping {
		var b protoBuf
		b.appendVarintField(1, mapping.Start)
		b.appendVarintField(2, mapping.Limit)
		b.appendVarintField(3, mapping.Offset)
		b.appendVarintField(4, uint64(enc.str(enc.path(mapping.File))))
		mappings[mapping] = int32(len(enc.mapping))
		enc.mapping = append(enc.mapping, b.data)
	}
	locations := map[*pprof.Location]int32{}
	for _, location := range parsed.Location {
		var b protoBuf
		b.appendVarintField(1, uint64(mappings[location.Mapping]))
		b.appendVarintField(2, location.Address)
		for _, line := range location.Line {
			off := b.beginMsg(3)
			b.appendVarintField(1, uint64(functions[line.Function]))
			b.appendVarintField(2, uint64(line.Line))
			b.endMsg(off)
		}
		locations[location] = int32(len(enc.locations))
		enc.locations = append(enc.locations, b.data)
	}
	stackIndices := make([]int32, len(parsed.Sample))
	labelIndices := make([][]int32, len(parsed.Sample))
	for i, sample := range parsed.Sample {
		var stack protoBuf
		for _, location := range sample.Location {
			stack.appendVarintField(1, uint64(locations[location]))
		}
		stackIndices[i] = int32(len(enc.stacks))
		enc.stacks = append(enc.stacks, stack.data)
		if options.LabelFilter != nil {
			labelIndices[i] = enc.sampleAttributes(sample, options.LabelFilter)
		}
	}
	if enc.err != nil {
		return nil, enc.err
	}
	var b protoBuf
	rp := b.beginMsg(1)
	res := b.beginMsg(1)
	b.appendKV(1, "service.name", attr.StringValue(service))
	resource.Range(func(a attr.Attr) bool { b.appendKV(1, a.Key, a.Value); return true })
	b.endMsg(res)
	scope := b.beginMsg(2)
	typ := enc.attribute(attr.String("profile.type", profileType))
	if enc.err != nil {
		return nil, enc.err
	}
	duration := end.Sub(start)
	if duration < 0 {
		duration = 0
	}
	for i, sampleType := range parsed.SampleType {
		off := b.beginMsg(2)
		enc.valueType(&b, 1, sampleType)
		for j, sample := range parsed.Sample {
			so := b.beginMsg(2)
			b.appendVarintField(1, uint64(stackIndices[j]))
			b.appendVarintField(2, uint64(sample.Value[i]))
			for _, index := range labelIndices[j] {
				b.appendVarintField(3, uint64(index))
			}
			b.endMsg(so)
			if len(b.data) > maxProfileBytes {
				return nil, fmt.Errorf("otlp/profiles: encoded samples exceed limit")
			}
		}
		b.appendFixed64(3, uint64(start.UnixNano()))
		b.appendVarintField(4, uint64(duration))
		if parsed.PeriodType != nil {
			enc.valueType(&b, 5, parsed.PeriodType)
		}
		b.appendVarintField(6, uint64(parsed.Period))
		id := internal.NewTraceID()
		b.appendBytes(7, id[:])
		b.appendVarintField(11, uint64(typ))
		b.endMsg(off)
	}
	b.endMsg(scope)
	b.endMsg(rp)
	dictionary := b.beginMsg(2)
	for _, raw := range enc.mapping {
		appendProfileEntry(&b, 1, raw)
	}
	for _, raw := range enc.locations {
		appendProfileEntry(&b, 2, raw)
	}
	for _, raw := range enc.functions {
		appendProfileEntry(&b, 3, raw)
	}
	appendProfileEntry(&b, 4, nil)
	for _, s := range enc.strings {
		if len(s) > maxProfileBytes-len(b.data)-16 {
			return nil, fmt.Errorf("otlp/profiles: encoded strings exceed limit")
		}
		b.appendTag(5, 2)
		b.appendRawVarint(uint64(len(s)))
		b.data = append(b.data, s...)
	}
	for _, raw := range enc.attributes {
		if len(raw) > maxProfileBytes-len(b.data)-16 {
			return nil, fmt.Errorf("otlp/profiles: encoded attributes exceed limit")
		}
		appendProfileEntry(&b, 6, raw)
	}
	for _, raw := range enc.stacks {
		appendProfileEntry(&b, 7, raw)
	}
	b.endMsg(dictionary)
	if len(b.data) > maxProfileBytes {
		return nil, fmt.Errorf("otlp/profiles: encoded profile exceeds limit")
	}
	return b.data, nil
}

type profileDictionary struct {
	strings                                           []string
	stringIndex                                       map[string]int32
	mapping, locations, functions, stacks, attributes [][]byte
	options                                           ProfileEncodingOptions
	attributeBytes                                    int
	err                                               error
}

func (e *profileDictionary) str(s string) int32 {
	if i, ok := e.stringIndex[s]; ok {
		return i
	}
	i := int32(len(e.strings))
	e.strings = append(e.strings, s)
	e.stringIndex[s] = i
	return i
}
func (e *profileDictionary) path(s string) string {
	if e.options.SourcePaths {
		return s
	}
	if s == "" {
		return ""
	}
	return filepath.Base(s)
}
func (e *profileDictionary) valueType(b *protoBuf, field int, t *pprof.ValueType) {
	off := b.beginMsg(field)
	b.appendVarintField(1, uint64(e.str(t.Type)))
	b.appendVarintField(2, uint64(e.str(t.Unit)))
	b.endMsg(off)
}
func (e *profileDictionary) attribute(a attr.Attr) int32 { return e.attributeUnit(a, "") }
func (e *profileDictionary) attributeUnit(a attr.Attr, unit string) int32 {
	var b protoBuf
	b.appendVarintField(1, uint64(e.str(a.Key)))
	budget := profileValueBudget{nodes: 4096, bytes: maxProfileBytes - e.attributeBytes}
	if err := appendProfileValue(&b, 2, a.Value.AsAny(), 0, &budget); err != nil {
		e.err = err
		return 0
	}
	if unit != "" {
		b.appendVarintField(3, uint64(e.str(unit)))
	}
	if len(b.data) > maxProfileBytes-e.attributeBytes {
		e.err = fmt.Errorf("otlp/profiles: attribute bytes exceed limit")
		return 0
	}
	e.attributeBytes += len(b.data)
	i := int32(len(e.attributes))
	e.attributes = append(e.attributes, b.data)
	return i
}

func appendProfileEntry(b *protoBuf, field int, raw []byte) {
	off := b.beginMsg(field)
	b.data = append(b.data, raw...)
	b.endMsg(off)
}

type profileLabelValue struct {
	value any
	unit  string
}

func (e *profileDictionary) sampleAttributes(sample *pprof.Sample, filter func(attr.Attr) (attr.Attr, bool)) []int32 {
	if len(sample.Label)+len(sample.NumLabel) > 64 {
		e.err = fmt.Errorf("otlp/profiles: sample attribute count exceeds limit")
		return nil
	}
	keys := make(map[string]struct{}, len(sample.Label)+len(sample.NumLabel))
	for key := range sample.Label {
		keys[key] = struct{}{}
	}
	for key := range sample.NumLabel {
		keys[key] = struct{}{}
	}
	names := make([]string, 0, len(keys))
	for key := range keys {
		names = append(names, key)
	}
	sort.Strings(names)
	merged := make(map[string][]profileLabelValue, len(names))
	for _, key := range names {
		values := make([]profileLabelValue, 0, len(sample.Label[key])+len(sample.NumLabel[key]))
		for _, value := range sample.Label[key] {
			values = append(values, profileLabelValue{value: value})
		}
		for i, value := range sample.NumLabel[key] {
			unit := ""
			if i < len(sample.NumUnit[key]) {
				unit = sample.NumUnit[key][i]
			}
			values = append(values, profileLabelValue{value: value, unit: unit})
		}
		value, unit := profileLabelAttribute(values)
		clean, keep := filter(attr.Any(key, value))
		if !keep {
			continue
		}
		if !numericLabelValue(clean.Value.AsAny()) {
			unit = ""
		}
		merged[clean.Key] = append(merged[clean.Key], profileLabelValue{value: clean.Value.AsAny(), unit: unit})
	}
	names = names[:0]
	for key := range merged {
		names = append(names, key)
	}
	sort.Strings(names)
	indices := make([]int32, 0, len(names))
	for _, key := range names {
		values := merged[key]
		if len(values) > 64 {
			e.err = fmt.Errorf("otlp/profiles: merged attribute count exceeds limit")
			return nil
		}
		if len(values) == 1 {
			indices = append(indices, e.attributeUnit(attr.Any(key, values[0].value), values[0].unit))
			continue
		}
		flattened := make([]profileLabelValue, 0, len(values))
		for _, entry := range values {
			rv := reflect.ValueOf(entry.value)
			if rv.IsValid() && (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
				if rv.Len() > 64-len(flattened) {
					e.err = fmt.Errorf("otlp/profiles: merged attribute values exceed limit")
					return nil
				}
				for i := 0; i < rv.Len(); i++ {
					flattened = append(flattened, profileLabelValue{value: rv.Index(i).Interface(), unit: entry.unit})
				}
			} else {
				if len(flattened) >= 64 {
					e.err = fmt.Errorf("otlp/profiles: merged attribute values exceed limit")
					return nil
				}
				flattened = append(flattened, entry)
			}
		}
		value, unit := profileLabelAttribute(flattened)
		indices = append(indices, e.attributeUnit(attr.Any(key, value), unit))
	}
	return indices
}
func profileLabelAttribute(values []profileLabelValue) (any, string) {
	if len(values) == 0 {
		return []any{}, ""
	}
	unit := values[0].unit
	for _, entry := range values {
		if entry.unit != unit {
			unit = ""
			break
		}
	}
	out := make([]any, 0, len(values))
	for _, entry := range values {
		value := entry.value
		if entry.unit != "" && entry.unit != unit {
			value = map[string]any{"value": value, "unit": entry.unit}
		}
		out = append(out, value)
	}
	if len(out) == 1 {
		return out[0], unit
	}
	return out, unit
}
func numericLabelValue(value any) bool {
	rv := reflect.ValueOf(value)
	if !rv.IsValid() {
		return false
	}
	numeric := func(v reflect.Value) bool {
		if v.Kind() == reflect.Interface {
			if v.IsNil() {
				return false
			}
			v = v.Elem()
		}
		return (v.Kind() >= reflect.Int && v.Kind() <= reflect.Float64)
	}
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return numeric(rv)
	}
	for i := 0; i < rv.Len(); i++ {
		if !numeric(rv.Index(i)) {
			return false
		}
	}
	return true
}

type profileValueBudget struct{ nodes, bytes int }

func (b *profileValueBudget) reserve(size int) error {
	if size < 0 || size > b.bytes {
		return fmt.Errorf("otlp/profiles: attribute bytes exceed limit")
	}
	b.bytes -= size
	return nil
}
func appendProfileValue(b *protoBuf, field int, value any, depth int, budget *profileValueBudget) error {
	if budget.nodes <= 0 || depth > 8 {
		return fmt.Errorf("otlp/profiles: attribute traversal exceeds limit")
	}
	budget.nodes--
	if err := budget.reserve(64); err != nil {
		return err
	}
	rv := reflect.ValueOf(value)
	if !rv.IsValid() {
		off := b.beginMsg(field)
		b.endMsg(off)
		return nil
	}
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		if rv.Len() > 64 {
			return fmt.Errorf("otlp/profiles: attribute array exceeds limit")
		}
		off := b.beginMsg(field)
		array := b.beginMsg(5)
		for i := 0; i < rv.Len(); i++ {
			if err := appendProfileValue(b, 1, rv.Index(i).Interface(), depth+1, budget); err != nil {
				return err
			}
		}
		b.endMsg(array)
		b.endMsg(off)
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String || rv.Len() > 64 {
			return fmt.Errorf("otlp/profiles: attribute map exceeds limit")
		}
		keys := rv.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		off := b.beginMsg(field)
		list := b.beginMsg(6)
		for _, key := range keys {
			if err := budget.reserve(len(key.String())); err != nil {
				return err
			}
			kv := b.beginMsg(1)
			b.appendString(1, key.String())
			if err := appendProfileValue(b, 2, rv.MapIndex(key).Interface(), depth+1, budget); err != nil {
				return err
			}
			b.endMsg(kv)
		}
		b.endMsg(list)
		b.endMsg(off)
	default:
		primitive := attr.AnyValue(value)
		off := b.beginMsg(field)
		switch primitive.Kind() {
		case attr.KindString:
			text := primitive.AsString()
			if err := budget.reserve(len(text)); err != nil {
				return err
			}
			b.appendTag(1, 2)
			b.appendRawVarint(uint64(len(text)))
			b.data = append(b.data, text...)
		case attr.KindInt64:
			b.appendTag(3, 0)
			b.appendRawVarint(uint64(primitive.AsInt64()))
		case attr.KindUint64:
			b.appendTag(3, 0)
			b.appendRawVarint(primitive.AsUint64())
		case attr.KindBool:
			b.appendTag(2, 0)
			if primitive.AsBool() {
				b.appendRawVarint(1)
			} else {
				b.appendRawVarint(0)
			}
		case attr.KindFloat64:
			b.appendTag(4, 1)
			b.data = binary.LittleEndian.AppendUint64(b.data, math.Float64bits(primitive.AsFloat64()))
		default:
			return fmt.Errorf("otlp/profiles: unsupported attribute type")
		}
		b.endMsg(off)
	}
	return nil
}
