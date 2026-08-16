package h2c

import (
	"strings"
	"testing"
)

func TestDecodeHeadersStrictValidAndBounded(t *testing.T) {
	block := []byte{0x20, 0x88} // table size 0, then :status: 200

	headers, err := DecodeHeadersStrict(block, 42)
	if err != nil {
		t.Fatalf("DecodeHeadersStrict: %v", err)
	}
	if len(headers) != 1 || headers[0].name != ":status" || headers[0].value != "200" {
		t.Fatalf("headers = %#v, want :status: 200", headers)
	}
	if _, err := DecodeHeadersStrict(block, 41); err == nil || !strings.Contains(err.Error(), "header list") {
		t.Fatalf("limit error = %v, want decoded header-list error", err)
	}
}

func TestDecodeHeadersStrictRejectsMalformedIntegersAndStrings(t *testing.T) {
	tests := []struct {
		name  string
		block []byte
		want  string
	}{
		{name: "truncated integer", block: []byte{0xff}, want: "truncated integer"},
		{name: "overflow integer", block: []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}, want: "integer overflow"},
		{name: "missing name string", block: []byte{0x00}, want: "truncated string"},
		{name: "truncated name string", block: []byte{0x00, 0x05, 'a'}, want: "truncated string"},
		{name: "missing value string", block: append([]byte{0x00}, appendHpackString(nil, "x")...), want: "truncated string"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeHeadersStrict(tt.block, DefaultMaxHeaderListSize); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestDecodeHeadersStrictRejectsDynamicTableUse(t *testing.T) {
	tests := []struct {
		name  string
		block []byte
		want  string
	}{
		{name: "indexed dynamic field", block: []byte{0xbe}, want: "dynamic index 62"},
		{name: "dynamic indexed name", block: []byte{0x0f, 0x2f, 0x00}, want: "dynamic name index 62"},
		{name: "nonzero table size", block: []byte{0x21}, want: "exceeds configured maximum 0"},
		{name: "late table update", block: []byte{0x88, 0x20}, want: "after header field"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeHeadersStrict(tt.block, DefaultMaxHeaderListSize); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestDecodeHeadersStrictRejectsInvalidHuffman(t *testing.T) {
	makeHeader := func(encodedValue ...byte) []byte {
		block := appendLiteralNewName(nil, "grpc-status", "")
		block = block[:len(block)-1] // replace the empty raw value string
		return append(block, encodedValue...)
	}

	tests := []struct {
		name  string
		block []byte
		want  string
	}{
		{name: "EOS", block: makeHeader(0x84, 0xff, 0xff, 0xff, 0xff), want: "EOS"},
		{name: "non-one padding", block: makeHeader(0x81, 0x00), want: "padding"},
		{name: "padding too long", block: makeHeader(0x81, 0xff), want: "padding length"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeHeadersStrict(tt.block, DefaultMaxHeaderListSize); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestDecodeHeadersStrictBoundsHuffmanExpansion(t *testing.T) {
	// 0x07 is the valid one-byte Huffman representation of "0".
	if _, _, err := decodeHpackStringStrict([]byte{0x81, 0x07}, 0); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("error = %v, want Huffman output budget error", err)
	}
}

func TestDecodeHuffmanStrictRFCVectors(t *testing.T) {
	tests := []struct {
		encoded []byte
		want    string
	}{
		{encoded: []byte{0xf1, 0xe3, 0xc2, 0xe5, 0xf2, 0x3a, 0x6b, 0xa0, 0xab, 0x90, 0xf4, 0xff}, want: "www.example.com"},
		{encoded: []byte{0xa8, 0xeb, 0x10, 0x64, 0x9c, 0xbf}, want: "no-cache"},
		{encoded: []byte{0x25, 0xa8, 0x49, 0xe9, 0x5b, 0xa9, 0x7d, 0x7f}, want: "custom-key"},
		{encoded: []byte{0x25, 0xa8, 0x49, 0xe9, 0x5b, 0xb8, 0xe8, 0xb4, 0xbf}, want: "custom-value"},
	}
	for _, tt := range tests {
		got, err := decodeHuffmanStrict(tt.encoded, uint64(len(tt.want)))
		if err != nil {
			t.Errorf("decode %q: %v", tt.want, err)
			continue
		}
		if got != tt.want {
			t.Errorf("decoded = %q, want %q", got, tt.want)
		}
	}
}

func FuzzDecodeHeadersStrict(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x88})
	f.Add([]byte{0x20, 0x88})
	f.Add(appendLiteralNewName(nil, "grpc-status", "0"))
	f.Add([]byte{0xff})
	f.Add([]byte{0xbe})
	f.Add([]byte{0x00, 0x81, 0xff})

	f.Fuzz(func(t *testing.T, block []byte) {
		const limit = uint64(1024)
		headers, err := DecodeHeadersStrict(block, limit)
		if err != nil {
			return
		}
		var size uint64
		for _, h := range headers {
			size += uint64(len(h.name) + len(h.value) + 32)
		}
		if size > limit {
			t.Fatalf("successful decode used %d bytes, limit %d", size, limit)
		}
	})
}

func FuzzDecodeHuffmanStrict(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x07})
	f.Add([]byte{0xff})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})

	f.Fuzz(func(t *testing.T, encoded []byte) {
		const limit = uint64(128)
		decoded, err := decodeHuffmanStrict(encoded, limit)
		if err == nil && uint64(len(decoded)) > limit {
			t.Fatalf("successful decode produced %d bytes, limit %d", len(decoded), limit)
		}
	})
}
