// Package pprof reads bounded, uncompressed pprof protobuf profiles.
package pprof

import (
	"encoding/binary"
	"fmt"
	"unicode/utf8"
)

const maxBytes = 32 << 20
const maxItems = 1 << 20

type ValueType struct{ Type, Unit string }
type Function struct {
	Name, SystemName, Filename string
	StartLine                  int64
}
type Mapping struct {
	Start, Limit, Offset uint64
	File                 string
}
type Line struct {
	Function *Function
	Line     int64
}
type Location struct {
	Mapping *Mapping
	Address uint64
	Line    []Line
}
type Sample struct {
	Location []*Location
	Value    []int64
	Label    map[string][]string
	NumLabel map[string][]int64
	NumUnit  map[string][]string
}
type Profile struct {
	SampleType []*ValueType
	Function   []*Function
	Mapping    []*Mapping
	Location   []*Location
	Sample     []*Sample
	PeriodType *ValueType
	Period     int64
}
type field struct {
	number int
	wire   int
	value  uint64
	data   []byte
}
type decoder struct{ items, memory int }

func invalid() error { return fmt.Errorf("invalid or oversized pprof protobuf") }
func (d *decoder) reserve(n int) error {
	d.items++
	if d.items > maxItems || n > (128<<20)-d.memory {
		return invalid()
	}
	d.memory += n
	return nil
}
func (d *decoder) fields(raw []byte, visit func(field) error) error {
	for len(raw) > 0 {
		if err := d.reserve(0); err != nil {
			return err
		}
		tag, n := binary.Uvarint(raw)
		if n <= 0 || tag>>3 == 0 || tag>>3 > (1<<29)-1 {
			return invalid()
		}
		raw = raw[n:]
		f := field{number: int(tag >> 3), wire: int(tag & 7)}
		switch f.wire {
		case 0:
			f.value, n = binary.Uvarint(raw)
			if n <= 0 {
				return invalid()
			}
			raw = raw[n:]
		case 1:
			if len(raw) < 8 {
				return invalid()
			}
			raw = raw[8:]
		case 2:
			size, k := binary.Uvarint(raw)
			if k <= 0 || size > uint64(len(raw)-k) {
				return invalid()
			}
			raw = raw[k:]
			f.data = raw[:int(size)]
			raw = raw[int(size):]
		case 5:
			if len(raw) < 4 {
				return invalid()
			}
			raw = raw[4:]
		default:
			return invalid()
		}
		if err := visit(f); err != nil {
			return err
		}
	}
	return nil
}
func scalar(f field) (uint64, error) {
	if f.wire != 0 {
		return 0, invalid()
	}
	return f.value, nil
}
func (d *decoder) repeated(f field, out *[]uint64) error {
	if f.wire == 0 {
		if err := d.reserve(24); err != nil {
			return err
		}
		*out = append(*out, f.value)
		return nil
	}
	if f.wire != 2 {
		return invalid()
	}
	for raw := f.data; len(raw) > 0; {
		v, n := binary.Uvarint(raw)
		if n <= 0 {
			return invalid()
		}
		if err := d.reserve(24); err != nil {
			return err
		}
		*out = append(*out, v)
		raw = raw[n:]
	}
	return nil
}

type record struct {
	values   [15]uint64
	repeated [15][]uint64
	children [15][]*record
	text     string
}

func (d *decoder) record(raw []byte, kind int) (*record, error) {
	if err := d.reserve(1536); err != nil {
		return nil, err
	}
	r := new(record)
	err := d.fields(raw, func(f field) error {
		child := 0
		repeat := false
		known := false
		switch kind {
		case 0:
			switch f.number {
			case 1:
				child = 1
			case 2:
				child = 2
			case 3:
				child = 3
			case 4:
				child = 4
			case 5:
				child = 5
			case 11:
				child = 1
			case 6:
				if f.wire != 2 || !utf8.Valid(f.data) {
					return invalid()
				}
				if err := d.reserve(len(f.data) + 1536); err != nil {
					return err
				}
				r.children[6] = append(r.children[6], &record{text: string(f.data)})
				return nil
			case 7, 8, 9, 10, 12, 14:
				known = true
			case 13:
				repeat = true
			}
		case 1:
			known = f.number == 1 || f.number == 2
		case 2:
			switch f.number {
			case 1, 2:
				repeat = true
			case 3:
				child = 6
			}
		case 3:
			known = f.number >= 1 && f.number <= 10
		case 4:
			switch f.number {
			case 1, 2, 3, 5:
				known = true
			case 4:
				child = 7
			}
		case 5:
			known = f.number >= 1 && f.number <= 5
		case 6:
			known = f.number >= 1 && f.number <= 4
		case 7:
			known = f.number >= 1 && f.number <= 3
		}
		if child != 0 {
			if f.wire != 2 {
				return invalid()
			}
			c, err := d.record(f.data, child)
			if err != nil {
				return err
			}
			r.children[f.number] = append(r.children[f.number], c)
			return nil
		}
		if repeat {
			return d.repeated(f, &r.repeated[f.number])
		}
		if known {
			v, err := scalar(f)
			if err != nil {
				return err
			}
			r.values[f.number] = v
		}
		return nil
	})
	return r, err
}

// ParseUncompressed validates and resolves pprof protobuf records.
func ParseUncompressed(raw []byte) (*Profile, error) {
	if len(raw) > maxBytes {
		return nil, invalid()
	}
	d := new(decoder)
	r, err := d.record(raw, 0)
	if err != nil {
		return nil, err
	}
	strings := r.children[6]
	if len(strings) == 0 || strings[0].text != "" {
		return nil, invalid()
	}
	str := func(index uint64) (string, error) {
		if index >= uint64(len(strings)) {
			return "", invalid()
		}
		return strings[index].text, nil
	}
	var failure error
	get := func(index uint64) string {
		s, e := str(index)
		if e != nil {
			failure = e
		}
		return s
	}
	p := new(Profile)
	valueType := func(v *record) *ValueType { return &ValueType{Type: get(v.values[1]), Unit: get(v.values[2])} }
	for _, v := range r.children[1] {
		p.SampleType = append(p.SampleType, valueType(v))
	}
	if len(p.SampleType) == 0 {
		return nil, invalid()
	}
	if vs := r.children[11]; len(vs) > 0 {
		p.PeriodType = valueType(vs[len(vs)-1])
	}
	p.Period = int64(r.values[12])
	for _, i := range []int{7, 8, 14} {
		get(r.values[i])
	}
	for _, v := range r.repeated[13] {
		get(v)
	}
	functions := map[uint64]*Function{}
	mappings := map[uint64]*Mapping{}
	locations := map[uint64]*Location{}
	for _, v := range r.children[5] {
		id := v.values[1]
		if id == 0 || functions[id] != nil {
			return nil, invalid()
		}
		f := &Function{Name: get(v.values[2]), SystemName: get(v.values[3]), Filename: get(v.values[4]), StartLine: int64(v.values[5])}
		functions[id] = f
		p.Function = append(p.Function, f)
	}
	for _, v := range r.children[3] {
		id := v.values[1]
		if id == 0 || mappings[id] != nil {
			return nil, invalid()
		}
		get(v.values[6])
		m := &Mapping{Start: v.values[2], Limit: v.values[3], Offset: v.values[4], File: get(v.values[5])}
		mappings[id] = m
		p.Mapping = append(p.Mapping, m)
	}
	for _, v := range r.children[4] {
		id := v.values[1]
		if id == 0 || locations[id] != nil {
			return nil, invalid()
		}
		l := &Location{Mapping: mappings[v.values[2]], Address: v.values[3]}
		if v.values[2] != 0 && l.Mapping == nil {
			return nil, invalid()
		}
		for _, line := range v.children[4] {
			f := functions[line.values[1]]
			if f == nil {
				return nil, invalid()
			}
			l.Line = append(l.Line, Line{Function: f, Line: int64(line.values[2])})
		}
		locations[id] = l
		p.Location = append(p.Location, l)
	}
	for _, v := range r.children[2] {
		if len(v.repeated[2]) != len(p.SampleType) {
			return nil, invalid()
		}
		s := &Sample{Label: map[string][]string{}, NumLabel: map[string][]int64{}, NumUnit: map[string][]string{}}
		for _, id := range v.repeated[1] {
			l := locations[id]
			if l == nil {
				return nil, invalid()
			}
			s.Location = append(s.Location, l)
		}
		for _, n := range v.repeated[2] {
			s.Value = append(s.Value, int64(n))
		}
		for _, label := range v.children[3] {
			key := get(label.values[1])
			text := get(label.values[2])
			unit := get(label.values[4])
			numeric := label.values[3] != 0 || label.values[4] != 0
			if label.values[2] != 0 && numeric {
				return nil, invalid()
			}
			if label.values[2] != 0 {
				s.Label[key] = append(s.Label[key], text)
			} else if numeric {
				s.NumLabel[key] = append(s.NumLabel[key], int64(label.values[3]))
				s.NumUnit[key] = append(s.NumUnit[key], unit)
			}
		}
		p.Sample = append(p.Sample, s)
	}
	if failure != nil {
		return nil, failure
	}
	return p, nil
}

// CheckValid checks resolved sample dimensions.
func (p *Profile) CheckValid() error {
	if p == nil || len(p.SampleType) == 0 {
		return invalid()
	}
	for _, s := range p.Sample {
		if s == nil || len(s.Value) != len(p.SampleType) {
			return invalid()
		}
	}
	return nil
}
