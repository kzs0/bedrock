package pprof

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func FuzzParseUncompressed(f *testing.F) {
	normal := testProfile(append(testVarint(1, 1), testVarint(2, 2)...))
	packed := testMessage(1, nil)
	packed = append(packed, testMessage(2, testMessage(2, binary.AppendUvarint(nil, ^uint64(0))))...)
	packed = append(packed, testMessage(6, nil)...)
	for _, seed := range [][]byte{normal, packed, {}, {0x80}, {0x0f}, normal[:len(normal)-1]} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 64<<10 {
			t.Skip()
		}
		original := bytes.Clone(raw)
		parsed, err := ParseUncompressed(raw)
		if !bytes.Equal(raw, original) {
			t.Fatal("parser mutated input")
		}
		if err == nil {
			if parsed == nil {
				t.Fatal("successful parse returned nil")
			}
			if err := parsed.CheckValid(); err != nil {
				t.Fatalf("successful parse failed validation: %v", err)
			}
		}
	})
}
