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

// ── Frame parsing tests ───────────────────────────────────────────────────────

func TestParseFrameHeader(t *testing.T) {
	tests := []struct {
		name     string
		hdr      [9]byte
		wantLen  uint32
		wantTyp  uint8
		wantFlag uint8
		wantSID  uint32
	}{
		{
			name:    "data frame, stream 1",
			hdr:     [9]byte{0, 0, 5, frameTypeData, flagEndStream, 0, 0, 0, 1},
			wantLen: 5, wantTyp: frameTypeData, wantFlag: flagEndStream, wantSID: 1,
		},
		{
			name:    "settings frame, stream 0",
			hdr:     [9]byte{0, 0, 12, frameTypeSettings, 0, 0, 0, 0, 0},
			wantLen: 12, wantTyp: frameTypeSettings, wantFlag: 0, wantSID: 0,
		},
		{
			name: "reserved bit stripped from stream ID",
			// Stream ID with MSB set (reserved) should be masked off
			hdr:     [9]byte{0, 0, 0, frameTypeHeaders, flagEndHeaders, 0x80, 0, 0, 3},
			wantLen: 0, wantTyp: frameTypeHeaders, wantFlag: flagEndHeaders, wantSID: 3,
		},
		{
			name:    "large payload length",
			hdr:     [9]byte{0x00, 0x40, 0x00, frameTypeData, 0, 0, 0, 0, 1},
			wantLen: 0x4000, wantTyp: frameTypeData, wantFlag: 0, wantSID: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := parseFrameHeader(tt.hdr)
			if f.length != tt.wantLen {
				t.Errorf("length: got %d, want %d", f.length, tt.wantLen)
			}
			if f.typ != tt.wantTyp {
				t.Errorf("typ: got %d, want %d", f.typ, tt.wantTyp)
			}
			if f.flags != tt.wantFlag {
				t.Errorf("flags: got %d, want %d", f.flags, tt.wantFlag)
			}
			if f.streamID != tt.wantSID {
				t.Errorf("streamID: got %d, want %d", f.streamID, tt.wantSID)
			}
		})
	}
}

func TestBuildFrame(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5}
	out := buildFrame(frameTypeData, flagEndStream, 7, payload)

	if len(out) != 9+len(payload) {
		t.Fatalf("frame length: got %d, want %d", len(out), 9+len(payload))
	}
	// Parse back
	var hdr [9]byte
	copy(hdr[:], out[:9])
	f := parseFrameHeader(hdr)

	if f.length != uint32(len(payload)) {
		t.Errorf("payload length: got %d, want %d", f.length, len(payload))
	}
	if f.typ != frameTypeData {
		t.Errorf("type: got %d, want %d", f.typ, frameTypeData)
	}
	if f.flags != flagEndStream {
		t.Errorf("flags: got %d, want %d", f.flags, flagEndStream)
	}
	if f.streamID != 7 {
		t.Errorf("streamID: got %d, want 7", f.streamID)
	}
	if string(out[9:]) != string(payload) {
		t.Errorf("payload mismatch")
	}
}

func TestBuildFrame_EmptyPayload(t *testing.T) {
	out := buildFrame(frameTypeSettings, flagAck, 0, nil)
	if len(out) != 9 {
		t.Errorf("empty payload frame: got %d bytes, want 9", len(out))
	}
	var hdr [9]byte
	copy(hdr[:], out)
	f := parseFrameHeader(hdr)
	if f.length != 0 {
		t.Errorf("length: got %d, want 0", f.length)
	}
}

func TestEncodeSettings(t *testing.T) {
	params := []settingParam{
		{settingEnablePush, 0},
		{settingInitialWindowSize, 65535},
	}
	b := encodeSettings(params)
	if len(b) != 12 {
		t.Errorf("expected 12 bytes (2 params × 6), got %d", len(b))
	}
	// First param: id=2, val=0
	if b[0] != 0 || b[1] != 2 {
		t.Errorf("first param id: got %02x%02x, want 0002", b[0], b[1])
	}
	if b[2] != 0 || b[3] != 0 || b[4] != 0 || b[5] != 0 {
		t.Errorf("first param val should be 0, got %v", b[2:6])
	}
	// Second param: id=4, val=65535
	if b[6] != 0 || b[7] != 4 {
		t.Errorf("second param id: got %02x%02x, want 0004", b[6], b[7])
	}
}

// ── Huffman decode edge cases ─────────────────────────────────────────────────

func TestDecodeHuffman_Empty(t *testing.T) {
	got := decodeHuffman(nil)
	if got != "" {
		t.Errorf("decodeHuffman(nil): got %q, want empty", got)
	}
	got = decodeHuffman([]byte{})
	if got != "" {
		t.Errorf("decodeHuffman([]byte{}): got %q, want empty", got)
	}
}

func TestDecodeHuffman_MultiChar(t *testing.T) {
	// Encode "hello" using HPACK Huffman table and verify round-trip via header decode.
	// We build a header block with Huffman-encoded value and decode it.
	//
	// Instead of manually crafting Huffman bytes, use HPACK Huffman decode
	// of a known test vector: "no" in Huffman.
	//   'n' = 0x2a (6 bits), 'o' = 0x7 (5 bits)
	//   packed: 101010 00111 + padding 11111 = 10101000 11111111 = 0xa8 0xff
	got := decodeHuffman([]byte{0xa8, 0xff})
	if got != "no" {
		t.Errorf("decodeHuffman 'no': got %q, want 'no'", got)
	}
}

func TestDecodeHuffman_ViaHeaderDecode(t *testing.T) {
	// Build a header block where the value is Huffman-encoded "0"
	// and verify the full header round-trip decodes correctly.
	// '0' = 0x0 (5 bits), padded: 00000 111 = 0x07
	huffValue := []byte{0x81, 0x07} // H=1 | len=1, value=0x07

	block := []byte{0x00}
	block = append(block, appendHpackString(nil, "x-custom")...)
	block = append(block, huffValue...)

	headers := DecodeHeaders(block)
	if len(headers) != 1 {
		t.Fatalf("expected 1 header, got %d", len(headers))
	}
	if headers[0].name != "x-custom" {
		t.Errorf("name: got %q, want x-custom", headers[0].name)
	}
	if headers[0].value != "0" {
		t.Errorf("value: got %q, want '0'", headers[0].value)
	}
}

func TestDecodeHeaders_IncrementalIndexing(t *testing.T) {
	// Test literal with incremental indexing (0x40 prefix)
	// Format: 0x40 (new name, 6-bit prefix with index 0), then name string, then value string
	name := "x-test"
	value := "hello"
	// Incremental indexing with new name: 0x40 | 0 = 0x40
	block := []byte{0x40}
	block = append(block, appendHpackString(nil, name)...)
	block = append(block, appendHpackString(nil, value)...)

	headers := DecodeHeaders(block)
	if len(headers) != 1 {
		t.Fatalf("expected 1 header, got %d: %v", len(headers), headers)
	}
	if headers[0].name != name {
		t.Errorf("name: got %q, want %q", headers[0].name, name)
	}
	if headers[0].value != value {
		t.Errorf("value: got %q, want %q", headers[0].value, value)
	}
}

func TestDecodeHeaders_Empty(t *testing.T) {
	headers := DecodeHeaders(nil)
	if len(headers) != 0 {
		t.Errorf("expected 0 headers for nil block, got %d", len(headers))
	}
}

func TestDecodeHeaders_MultipleHeaders(t *testing.T) {
	// Encode two headers and decode them
	var block []byte
	// :status 200 (static index 8 → 0x88)
	block = append(block, 0x88)
	// grpc-status: 0
	block = append(block, 0x00)
	block = append(block, appendHpackString(nil, "grpc-status")...)
	block = append(block, appendHpackString(nil, "0")...)

	headers := DecodeHeaders(block)
	if len(headers) != 2 {
		t.Fatalf("expected 2 headers, got %d: %v", len(headers), headers)
	}
	if headers[0].name != ":status" || headers[0].value != "200" {
		t.Errorf("header[0]: got %q:%q", headers[0].name, headers[0].value)
	}
	if headers[1].name != "grpc-status" || headers[1].value != "0" {
		t.Errorf("header[1]: got %q:%q", headers[1].name, headers[1].value)
	}
}

func TestAppendHpackString_LongString(t *testing.T) {
	// Test that strings >= 128 bytes use multi-byte length encoding
	long := make([]byte, 200)
	for i := range long {
		long[i] = 'a'
	}
	s := string(long)
	encoded := appendHpackString(nil, s)

	// First byte should be 0x7f (127 as the 7-bit max)
	if encoded[0] != 0x7f {
		t.Errorf("first byte: got 0x%02x, want 0x7f", encoded[0])
	}
	// Decode it back
	decoded, n := decodeHpackString(encoded)
	if n != len(encoded) {
		t.Errorf("consumed %d bytes, expected %d", n, len(encoded))
	}
	if decoded != s {
		t.Errorf("decoded string mismatch")
	}
}
