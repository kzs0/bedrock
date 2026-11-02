package otlp

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
)

func profileWireVarint(field int, value uint64) []byte {
	b := binary.AppendUvarint(nil, uint64(field<<3))
	return binary.AppendUvarint(b, value)
}

func profileWireBytes(field int, value []byte) []byte {
	b := binary.AppendUvarint(nil, uint64(field<<3|2))
	b = binary.AppendUvarint(b, uint64(len(value)))
	return append(b, value...)
}

func profileWireJoin(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func profileWireFixture(sample, mapping, location, function []byte) []byte {
	return profileWireJoin(
		profileWireBytes(1, profileWireJoin(profileWireVarint(1, 1), profileWireVarint(2, 2))),
		profileWireBytes(2, sample), profileWireBytes(3, mapping),
		profileWireBytes(4, location), profileWireBytes(5, function),
		profileWireBytes(6, nil), profileWireBytes(6, []byte("samples")),
		profileWireBytes(6, []byte("count")),
	)
}

func TestEncodeProfileCheckedWireValidation(t *testing.T) {
	v, b, join := profileWireVarint, profileWireBytes, profileWireJoin
	sample := join(v(1, 3), v(2, 7))
	mapping := v(1, 9)
	function := join(v(1, 11), v(2, 1))
	location := join(v(1, 3), v(2, 9), b(4, v(1, 11)))
	fixture := func(s, m, l, f []byte) []byte { return profileWireFixture(s, m, l, f) }
	valid := fixture(sample, mapping, location, function)
	tests := []struct {
		name string
		raw  []byte
	}{
		{"truncated tag", append(bytes.Clone(valid), 0x80)},
		{"overflow tag", append(bytes.Clone(valid), bytes.Repeat([]byte{0xff}, 11)...)},
		{"truncated length", append(bytes.Clone(valid), 0x32, 0x80)},
		{"length beyond input", append(bytes.Clone(valid), 0x32, 0x7f)},
		{"invalid wire type", append(bytes.Clone(valid), 0x3f)},
		{"zero field number", append(bytes.Clone(valid), 0)},
		{"wrong sample wire type", append(bytes.Clone(valid), v(2, 1)...)},
		{"truncated nested value", fixture(join(v(1, 3), []byte{0x10, 0x80}), mapping, location, function)},
		{"overflow nested value", fixture(join(v(1, 3), []byte{0x10}, bytes.Repeat([]byte{0xff}, 10)), mapping, location, function)},
		{"missing location id", fixture(sample, mapping, v(2, 9), function)},
		{"zero location id", fixture(sample, mapping, v(1, 0), function)},
		{"duplicate location id", append(bytes.Clone(valid), b(4, location)...)},
		{"duplicate mapping id", append(bytes.Clone(valid), b(3, mapping)...)},
		{"duplicate function id", append(bytes.Clone(valid), b(5, function)...)},
		{"missing mapping id", fixture(sample, nil, location, function)},
		{"zero mapping id", fixture(sample, v(1, 0), location, function)},
		{"zero function id", fixture(sample, mapping, location, v(1, 0))},
		{"missing function id", fixture(sample, mapping, location, v(2, 1))},
		{"unresolved location", fixture(join(v(1, 99), v(2, 7)), mapping, location, function)},
		{"unresolved mapping", fixture(sample, mapping, join(v(1, 3), v(2, 99)), function)},
		{"unresolved function", fixture(sample, mapping, join(v(1, 3), b(4, v(1, 99))), function)},
		{"invalid function string", fixture(sample, mapping, location, join(v(1, 11), v(2, 99)))},
		{"negative function string", fixture(sample, mapping, location, join(v(1, 11), v(2, ^uint64(0))))},
		{"invalid label key", fixture(join(sample, b(3, join(v(1, 99), v(2, 1)))), mapping, location, function)},
		{"invalid label string", fixture(join(sample, b(3, join(v(1, 1), v(2, 99)))), mapping, location, function)},
		{"invalid label unit", fixture(join(sample, b(3, join(v(1, 1), v(3, 2), v(4, 99)))), mapping, location, function)},
		{"missing sample values", fixture(v(1, 3), mapping, location, function)},
		{"extra sample values", fixture(join(sample, v(2, 8)), mapping, location, function)},
		{"invalid sample type string", append(bytes.Clone(valid), b(1, v(1, 99))...)},
		{"invalid comment string", append(bytes.Clone(valid), v(13, 99)...)},
		{"invalid default sample type", append(bytes.Clone(valid), v(14, 99)...)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := EncodeProfileChecked("svc", attr.EmptySet, "heap", tt.raw, time.Unix(1, 0), time.Unix(2, 0), ProfileEncodingOptions{})
			if err == nil || len(payload) != 0 {
				t.Fatalf("malformed profile exported: bytes=%d err=%v", len(payload), err)
			}
		})
	}
}

func TestEncodeProfileCheckedValidWireForms(t *testing.T) {
	v, b, join := profileWireVarint, profileWireBytes, profileWireJoin
	for _, packed := range []bool{false, true} {
		for _, value := range []int64{-7, 0, 7} {
			var sample []byte
			if packed {
				sample = join(b(1, binary.AppendUvarint(binary.AppendUvarint(nil, 3), 3)), b(2, binary.AppendUvarint(nil, uint64(value))))
			} else {
				sample = join(v(1, 3), v(1, 3), v(2, uint64(value)))
			}
			raw := profileWireFixture(sample, v(1, 9), join(v(1, 3), v(2, 9), b(4, v(1, 11))), join(v(1, 11), v(2, 1)))
			raw = append(raw, v(100, 42)...)
			raw = append(raw, b(101, []byte("ignored"))...)
			raw = append(raw, binary.LittleEndian.AppendUint64([]byte{0xb1, 0x06}, 42)...)
			raw = append(raw, []byte{0xbd, 0x06, 1, 2, 3, 4}...)
			for _, compressed := range []bool{false, true} {
				input := raw
				if compressed {
					var buffer bytes.Buffer
					writer := gzip.NewWriter(&buffer)
					if _, err := writer.Write(raw); err != nil {
						t.Fatal(err)
					}
					if err := writer.Close(); err != nil {
						t.Fatal(err)
					}
					input = buffer.Bytes()
				}
				payload, err := EncodeProfileChecked("svc", attr.EmptySet, "heap", input, time.Unix(1, 0), time.Unix(2, 0), ProfileEncodingOptions{})
				if err != nil || len(payload) == 0 {
					t.Fatalf("packed=%v value=%d gzip=%v: bytes=%d err=%v", packed, value, compressed, len(payload), err)
				}
			}
		}
	}
}
