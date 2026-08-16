package h2c

import (
	"fmt"
	"math"
)

// hpack.go implements the subset of HPACK (RFC 7541) needed for gRPC client requests.
//
// Encoding: uses non-Huffman literals only (H=0). Static table indexed references
// are used for :method, :scheme. All other headers are literal without indexing.
//
// Decoding: handles static-table indexed and literal headers, including HPACK
// Huffman strings. The client advertises a zero-sized dynamic table, so dynamic
// references and non-zero dynamic table size updates are compression errors.

// DefaultMaxHeaderListSize is the decoded response header-list budget used by
// the transport. Per RFC 7540, each field consumes its name and value lengths
// plus 32 bytes of overhead.
const DefaultMaxHeaderListSize uint64 = 64 << 10

// hpackStaticTable is the HPACK static header table (RFC 7541 Appendix A).
// Index 0 is unused; entries are 1-indexed as per the RFC.
var hpackStaticTable = [62]struct{ name, value string }{
	0:  {"", ""},
	1:  {":authority", ""},
	2:  {":method", "GET"},
	3:  {":method", "POST"},
	4:  {":path", "/"},
	5:  {":path", "/index.html"},
	6:  {":scheme", "http"},
	7:  {":scheme", "https"},
	8:  {":status", "200"},
	9:  {":status", "204"},
	10: {":status", "206"},
	11: {":status", "304"},
	12: {":status", "400"},
	13: {":status", "404"},
	14: {":status", "500"},
	15: {"accept-charset", ""},
	16: {"accept-encoding", "gzip, deflate"},
	17: {"accept-language", ""},
	18: {"accept-ranges", ""},
	19: {"accept", ""},
	20: {"access-control-allow-origin", ""},
	21: {"age", ""},
	22: {"allow", ""},
	23: {"authorization", ""},
	24: {"cache-control", ""},
	25: {"content-disposition", ""},
	26: {"content-encoding", ""},
	27: {"content-language", ""},
	28: {"content-length", ""},
	29: {"content-location", ""},
	30: {"content-range", ""},
	31: {"content-type", ""},
	32: {"cookie", ""},
	33: {"date", ""},
	34: {"etag", ""},
	35: {"expect", ""},
	36: {"expires", ""},
	37: {"from", ""},
	38: {"host", ""},
	39: {"if-match", ""},
	40: {"if-modified-since", ""},
	41: {"if-none-match", ""},
	42: {"if-range", ""},
	43: {"if-unmodified-since", ""},
	44: {"last-modified", ""},
	45: {"link", ""},
	46: {"location", ""},
	47: {"max-forwards", ""},
	48: {"proxy-authenticate", ""},
	49: {"proxy-authorization", ""},
	50: {"range", ""},
	51: {"referer", ""},
	52: {"refresh", ""},
	53: {"retry-after", ""},
	54: {"server", ""},
	55: {"set-cookie", ""},
	56: {"strict-transport-security", ""},
	57: {"transfer-encoding", ""},
	58: {"user-agent", ""},
	59: {"vary", ""},
	60: {"via", ""},
	61: {"www-authenticate", ""},
}

// EncodeRequestHeaders encodes gRPC request headers as an HPACK header block.
// Uses non-Huffman literals (H=0) and static table indexed references only.
//
// Encodes the following headers:
//
//	:method: POST          (static index 3)
//	:scheme: http/https    (static index 6 or 7)
//	:authority: <authority>
//	:path: <path>
//	content-type: application/grpc+proto
//	te: trailers
func EncodeRequestHeaders(authority, path string, tls bool) []byte {
	// Pre-size to hold all headers in one allocation:
	//   2  indexed (:method, :scheme)
	//   4  prefix+length bytes for :authority + authority string
	//   4  prefix+length bytes for :path + path string
	//  36  content-type: application/grpc+proto (literal new name + value)
	//  13  te: trailers
	b := make([]byte, 0, 2+4+len(authority)+4+len(path)+49)

	// :method: POST → indexed(3) = 0x83
	b = append(b, 0x83)

	// :scheme: http(6) / https(7) → indexed 0x86 / 0x87
	if tls {
		b = append(b, 0x87)
	} else {
		b = append(b, 0x86)
	}

	// :authority (index 1, literal value without indexing)
	b = appendLiteralIndexedName(b, 1, authority)

	// :path (index 4, literal value without indexing)
	b = appendLiteralIndexedName(b, 4, path)

	// content-type: application/grpc+proto (literal new name, without indexing)
	b = appendLiteralNewName(b, "content-type", "application/grpc+proto")

	// te: trailers
	b = appendLiteralNewName(b, "te", "trailers")

	return b
}

// appendLiteralIndexedName appends a literal header field without indexing,
// using an indexed name from the static table.
//
// Format: 0000NNNN [value string]  (N = 4-bit integer prefix for name index)
// For indices 1-14, the index fits in one byte.
func appendLiteralIndexedName(dst []byte, nameIndex uint8, value string) []byte {
	dst = append(dst, nameIndex&0x0f) // literal without indexing, indexed name
	return appendHpackString(dst, value)
}

// appendLiteralNewName appends a literal header field without indexing
// with a new name (not in any table).
//
// Format: 0x00 [name string] [value string]
func appendLiteralNewName(dst []byte, name, value string) []byte {
	dst = append(dst, 0x00)
	dst = appendHpackString(dst, name)
	return appendHpackString(dst, value)
}

// appendHpackString appends an HPACK string literal without Huffman encoding.
// Format: H=0 flag (0) | 7-bit length, followed by raw bytes.
// String lengths ≥ 128 use multi-byte integer encoding.
func appendHpackString(dst []byte, s string) []byte {
	n := len(s)
	if n < 128 {
		dst = append(dst, byte(n)) // H=0, length fits in 7 bits
	} else {
		// Multi-byte: first byte = 0x7f (127), then remainder as base-128 varint
		dst = append(dst, 0x7f)
		rem := uint(n - 127)
		for rem >= 0x80 {
			dst = append(dst, byte(rem)|0x80)
			rem >>= 7
		}
		dst = append(dst, byte(rem))
	}
	dst = append(dst, s...)
	return dst
}

// hpackHeader is a decoded header name-value pair.
type hpackHeader struct {
	name  string
	value string
}

// DecodeHeaders decodes an HPACK header block into a list of headers.
//
// It is retained for compatibility with existing package users. New transport
// code should call DecodeHeadersStrict so malformed input cannot be mistaken for
// a valid, incomplete header block.
func DecodeHeaders(block []byte) []hpackHeader {
	headers, _ := DecodeHeadersStrict(block, math.MaxUint64)
	return headers
}

// DecodeHeadersStrict decodes one complete HPACK header block. It rejects
// malformed encodings and enforces maxHeaderListSize using the RFC header-list
// accounting rule: len(name) + len(value) + 32 for every decoded field.
//
// This decoder intentionally has a zero-capacity dynamic table, matching the
// SETTINGS_HEADER_TABLE_SIZE value advertised by Client. A size update to zero
// is accepted only before the first header field; dynamic references and any
// non-zero size update are rejected.
func DecodeHeadersStrict(block []byte, maxHeaderListSize uint64) ([]hpackHeader, error) {
	var headers []hpackHeader
	var headerListSize uint64
	sawHeader := false

	for i := 0; i < len(block); {
		b := block[i]

		switch {
		case b&0x80 != 0:
			idx, n, err := decodeHpackIntStrict(block[i:], 7)
			if err != nil {
				return nil, fmt.Errorf("hpack: indexed field: %w", err)
			}
			if idx == 0 {
				return nil, fmt.Errorf("hpack: indexed field has zero index")
			}
			if idx >= uint64(len(hpackStaticTable)) {
				return nil, fmt.Errorf("hpack: dynamic index %d unavailable with zero-sized table", idx)
			}
			i += n
			e := hpackStaticTable[idx]
			if err := accountHeaderListSize(&headerListSize, maxHeaderListSize, e.name, e.value); err != nil {
				return nil, err
			}
			headers = append(headers, hpackHeader{e.name, e.value})
			sawHeader = true

		case b&0xc0 == 0x40:
			name, value, n, err := decodeLiteralHeaderStrict(block[i:], 6, remainingHeaderBytes(headerListSize, maxHeaderListSize))
			if err != nil {
				return nil, fmt.Errorf("hpack: incremental literal: %w", err)
			}
			i += n
			if err := accountHeaderListSize(&headerListSize, maxHeaderListSize, name, value); err != nil {
				return nil, err
			}
			headers = append(headers, hpackHeader{name, value})
			sawHeader = true

		case b&0xe0 == 0x20:
			size, n, err := decodeHpackIntStrict(block[i:], 5)
			if err != nil {
				return nil, fmt.Errorf("hpack: dynamic table size update: %w", err)
			}
			if sawHeader {
				return nil, fmt.Errorf("hpack: dynamic table size update after header field")
			}
			if size != 0 {
				return nil, fmt.Errorf("hpack: dynamic table size %d exceeds configured maximum 0", size)
			}
			i += n

		default:
			name, value, n, err := decodeLiteralHeaderStrict(block[i:], 4, remainingHeaderBytes(headerListSize, maxHeaderListSize))
			if err != nil {
				return nil, fmt.Errorf("hpack: literal: %w", err)
			}
			i += n
			if err := accountHeaderListSize(&headerListSize, maxHeaderListSize, name, value); err != nil {
				return nil, err
			}
			headers = append(headers, hpackHeader{name, value})
			sawHeader = true
		}
	}
	return headers, nil
}

func remainingHeaderBytes(used, limit uint64) uint64 {
	if used >= limit || limit-used <= 32 {
		return 0
	}
	return limit - used - 32
}

func accountHeaderListSize(used *uint64, limit uint64, name, value string) error {
	fieldSize := uint64(len(name)) + uint64(len(value)) + 32
	if *used > limit || fieldSize > limit-*used {
		return fmt.Errorf("hpack: decoded header list exceeds %d bytes", limit)
	}
	*used += fieldSize
	return nil
}

func decodeLiteralHeaderStrict(block []byte, prefixBits int, maxNameValueBytes uint64) (name, value string, n int, err error) {
	idx, n, err := decodeHpackIntStrict(block, prefixBits)
	if err != nil {
		return "", "", 0, err
	}

	remaining := maxNameValueBytes
	if idx == 0 {
		var consumed int
		var decodeErr error
		name, consumed, decodeErr = decodeHpackStringStrict(block[n:], remaining)
		if decodeErr != nil {
			return "", "", 0, fmt.Errorf("name: %w", decodeErr)
		}
		n += consumed
		if uint64(len(name)) > remaining {
			return "", "", 0, fmt.Errorf("name exceeds decoded field budget")
		}
		remaining -= uint64(len(name))
	} else {
		if idx >= uint64(len(hpackStaticTable)) {
			return "", "", 0, fmt.Errorf("dynamic name index %d unavailable with zero-sized table", idx)
		}
		name = hpackStaticTable[idx].name
		if uint64(len(name)) > remaining {
			return "", "", 0, fmt.Errorf("name exceeds decoded field budget")
		}
		remaining -= uint64(len(name))
	}

	value, consumed, err := decodeHpackStringStrict(block[n:], remaining)
	if err != nil {
		return "", "", 0, fmt.Errorf("value: %w", err)
	}
	n += consumed
	return name, value, n, nil
}

// decodeHpackInt decodes an HPACK integer with an N-bit prefix from block[0:].
// Returns (value, bytes_consumed).
func decodeHpackInt(block []byte, prefixBits int) (uint64, int) {
	value, n, _ := decodeHpackIntStrict(block, prefixBits)
	return value, n
}

func decodeHpackIntStrict(block []byte, prefixBits int) (uint64, int, error) {
	if prefixBits < 1 || prefixBits > 8 {
		return 0, 0, fmt.Errorf("invalid integer prefix width %d", prefixBits)
	}
	if len(block) == 0 {
		return 0, 0, fmt.Errorf("truncated integer")
	}
	mask := byte((1 << prefixBits) - 1)
	value := uint64(block[0] & mask)
	if value < uint64(mask) {
		return value, 1, nil
	}

	shift := uint(0)
	for i := 1; i < len(block); i++ {
		part := uint64(block[i] & 0x7f)
		if shift >= 64 || part > (math.MaxUint64-value)>>shift {
			return 0, 0, fmt.Errorf("integer overflow")
		}
		value += part << shift
		if block[i]&0x80 == 0 {
			return value, i + 1, nil
		}
		shift += 7
	}
	return 0, 0, fmt.Errorf("truncated integer")
}

// decodeHpackString decodes an HPACK string literal from block[0:].
// Returns ("", n) for Huffman-encoded strings (H=1) — caller should treat as opaque.
func decodeHpackString(block []byte) (string, int) {
	value, n, _ := decodeHpackStringStrict(block, math.MaxUint64)
	return value, n
}

func decodeHpackStringStrict(block []byte, maxDecodedBytes uint64) (string, int, error) {
	if len(block) == 0 {
		return "", 0, fmt.Errorf("truncated string")
	}
	huffman := block[0]&0x80 != 0
	length, n, err := decodeHpackIntStrict(block, 7)
	if err != nil {
		return "", 0, err
	}
	if length > uint64(len(block)-n) {
		return "", 0, fmt.Errorf("truncated string: declared %d bytes, have %d", length, len(block)-n)
	}
	raw := block[n : n+int(length)]
	n += int(length)
	if huffman {
		value, err := decodeHuffmanStrict(raw, maxDecodedBytes)
		if err != nil {
			return "", 0, err
		}
		return value, n, nil
	}
	if length > maxDecodedBytes {
		return "", 0, fmt.Errorf("decoded string exceeds %d-byte budget", maxDecodedBytes)
	}
	return string(raw), n, nil
}
