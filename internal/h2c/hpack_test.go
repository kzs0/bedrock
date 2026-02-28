package h2c

import (
	"testing"
)

func TestEncodeRequestHeaders_Cleartext(t *testing.T) {
	block := EncodeRequestHeaders("localhost:4317", "/test.Service/Method", false)
	if len(block) == 0 {
		t.Fatal("expected non-empty header block")
	}

	// First byte should be 0x83 (:method: POST indexed)
	if block[0] != 0x83 {
		t.Errorf("expected first byte 0x83 (:method: POST), got 0x%02x", block[0])
	}
	// Second byte should be 0x86 (:scheme: http indexed)
	if block[1] != 0x86 {
		t.Errorf("expected second byte 0x86 (:scheme: http), got 0x%02x", block[1])
	}
}

func TestEncodeRequestHeaders_TLS(t *testing.T) {
	block := EncodeRequestHeaders("example.com:443", "/svc/Method", true)
	if block[1] != 0x87 {
		t.Errorf("expected 0x87 (:scheme: https) for TLS, got 0x%02x", block[1])
	}
}

func TestDecodeHeaders_IndexedStatus200(t *testing.T) {
	// HPACK static table index 8 = ":status: 200", indexed representation = 0x88
	block := []byte{0x88}
	headers := DecodeHeaders(block)
	if len(headers) != 1 {
		t.Fatalf("expected 1 header, got %d", len(headers))
	}
	if headers[0].name != ":status" || headers[0].value != "200" {
		t.Errorf("expected :status:200, got %q:%q", headers[0].name, headers[0].value)
	}
}

func TestDecodeHeaders_LiteralNoHuffman(t *testing.T) {
	// Encode a literal header without indexing, new name: grpc-status: 0
	// 0x00, then "grpc-status" as H=0 string, then "0" as H=0 string
	name := "grpc-status"
	value := "0"
	block := []byte{0x00}
	block = append(block, appendHpackString(nil, name)...)
	block = append(block, appendHpackString(nil, value)...)

	headers := DecodeHeaders(block)
	if len(headers) != 1 {
		t.Fatalf("expected 1 header, got %d: %v", len(headers), headers)
	}
	if headers[0].name != "grpc-status" {
		t.Errorf("expected name grpc-status, got %q", headers[0].name)
	}
	if headers[0].value != "0" {
		t.Errorf("expected value 0, got %q", headers[0].value)
	}
}

func TestDecodeHeaders_HuffmanValue(t *testing.T) {
	// Encode a header where the VALUE is Huffman-encoded.
	// Use grpc-status name (literal new name, no Huffman for name)
	// and encode the value "0" with Huffman.
	// Huffman code for '0' is 0x0 (5 bits) → padded to 1 byte: 0b00000111 = pad to 8 bits
	// Huffman-encoded "0": H=1 flag on length byte; length=1 byte; data=0x07 (00000 + padding 111)
	name := "grpc-status"
	// "0" Huffman: code=0x0 (5 bits), packed into 1 byte: 00000 padded with 1s = 0x07
	huffValue := []byte{0x81, 0x07} // H=1 flag | length=1, then 0x07

	block := []byte{0x00}
	block = append(block, appendHpackString(nil, name)...)
	block = append(block, huffValue...)

	headers := DecodeHeaders(block)
	if len(headers) != 1 {
		t.Fatalf("expected 1 header, got %d", len(headers))
	}
	if headers[0].name != "grpc-status" {
		t.Errorf("expected grpc-status, got %q", headers[0].name)
	}
	if headers[0].value != "0" {
		t.Errorf("expected Huffman-decoded value '0', got %q", headers[0].value)
	}
}

func TestDecodeHuffman_CommonStrings(t *testing.T) {
	tests := []struct {
		input []byte
		want  string
	}{
		// '0' = code 0x0 (5 bits) packed into one byte with 3 padding bits (1s per RFC)
		// 00000 + 111 = 0x07
		{[]byte{0x07}, "0"},
		// 'a' = code 0x3 (5 bits): 00011 + 111 = 0x1f
		{[]byte{0x1f}, "a"},
	}
	for _, tt := range tests {
		got := decodeHuffman(tt.input)
		if got != tt.want {
			t.Errorf("decodeHuffman(%v): got %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseGRPCStatus(t *testing.T) {
	tests := []struct{ s string; want int }{
		{"0", 0},
		{"1", 1},
		{"13", 13},
		{"", 0},
		{"abc", 0},
	}
	for _, tt := range tests {
		got := parseGRPCStatus(tt.s)
		if got != tt.want {
			t.Errorf("parseGRPCStatus(%q) = %d, want %d", tt.s, got, tt.want)
		}
	}
}

func TestDecodeHpackInt(t *testing.T) {
	tests := []struct {
		block       []byte
		prefixBits  int
		wantVal     uint64
		wantN       int
	}{
		{[]byte{0x05}, 4, 5, 1},            // simple 4-bit: 0000 0101 & 0xf = 5
		{[]byte{0x0f, 0x01}, 4, 16, 2},     // overflow: 15 + 1 = 16
		{[]byte{0x83}, 7, 3, 1},            // 7-bit prefix: 0x83 & 0x7f = 3
	}
	for _, tt := range tests {
		val, n := decodeHpackInt(tt.block, tt.prefixBits)
		if val != tt.wantVal || n != tt.wantN {
			t.Errorf("decodeHpackInt(%v, %d) = (%d, %d), want (%d, %d)",
				tt.block, tt.prefixBits, val, n, tt.wantVal, tt.wantN)
		}
	}
}
