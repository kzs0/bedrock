package pprof

import (
	"encoding/binary"
	"testing"
)

func testVarint(field int, value uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(nil, uint64(field<<3)), value)
}

func testMessage(field int, data []byte) []byte {
	out := binary.AppendUvarint(nil, uint64(field<<3|2))
	out = binary.AppendUvarint(out, uint64(len(data)))
	return append(out, data...)
}

func testProfile(label []byte) []byte {
	raw := testMessage(1, nil)
	sample := append(testVarint(2, 7), testMessage(3, label)...)
	raw = append(raw, testMessage(2, sample)...)
	for _, value := range []string{"", "key", "text", "bytes"} {
		raw = append(raw, testMessage(6, []byte(value))...)
	}
	return raw
}

func TestUnsetLabelDoesNotBecomeNumericZero(t *testing.T) {
	p, err := ParseUncompressed(testProfile(testVarint(1, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Sample[0].NumLabel) != 0 || len(p.Sample[0].Label) != 0 {
		t.Fatalf("unset label acquired a value: %+v", p.Sample[0])
	}
}

func TestAmbiguousLabelKindsRejected(t *testing.T) {
	for _, numeric := range [][]byte{testVarint(3, 7), testVarint(4, 3)} {
		label := append(testVarint(1, 1), testVarint(2, 2)...)
		label = append(label, numeric...)
		if _, err := ParseUncompressed(testProfile(label)); err == nil {
			t.Fatal("ambiguous string and numeric label accepted")
		}
	}
}
