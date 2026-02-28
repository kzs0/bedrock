package h2c

// hpack.go implements the subset of HPACK (RFC 7541) needed for gRPC client requests.
//
// Encoding: uses non-Huffman literals only (H=0). Static table indexed references
// are used for :method, :scheme. All other headers are literal without indexing.
//
// Decoding: handles indexed (static table only) and H=0 literal headers.
// Huffman-encoded (H=1) strings are read and skipped; the header name/value will
// be empty in the decoded result. Dynamic table size is tracked but entries are
// not stored (we set SETTINGS_HEADER_TABLE_SIZE=0 to discourage server use).

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
// Dynamic table updates are tracked (size changes respected) but entries
// are discarded since we set SETTINGS_HEADER_TABLE_SIZE=0.
// Huffman-encoded string literals result in an empty name or value.
func DecodeHeaders(block []byte) []hpackHeader {
	var headers []hpackHeader
	i := 0
	dynTableSize := 0

	for i < len(block) {
		b := block[i]

		switch {
		case b&0x80 != 0:
			// Indexed representation: 1xxxxxxx
			idx, n := decodeHpackInt(block[i:], 7)
			i += n
			if idx > 0 && int(idx) < len(hpackStaticTable) {
				e := hpackStaticTable[idx]
				headers = append(headers, hpackHeader{e.name, e.value})
			}
			// Dynamic table lookups skipped (we don't store them)

		case b&0xc0 == 0x40:
			// Literal with incremental indexing: 01xxxxxx
			name, value, n := decodeLiteralHeader(block[i:], 6)
			i += n
			// Add to dynamic table (tracked for size, entry discarded)
			dynTableSize += 32 + len(name) + len(value)
			_ = dynTableSize
			headers = append(headers, hpackHeader{name, value})

		case b&0xe0 == 0x20:
			// Dynamic table size update: 001xxxxx
			_, n := decodeHpackInt(block[i:], 5)
			i += n

		default:
			// Literal without indexing (0x00) or never indexed (0x10)
			prefixBits := 4
			name, value, n := decodeLiteralHeader(block[i:], prefixBits)
			i += n
			headers = append(headers, hpackHeader{name, value})
		}
	}
	return headers
}

// decodeLiteralHeader decodes a literal header field from block[0:].
// prefixBits is the number of bits used as the integer prefix for the name index.
// Returns the (name, value, bytes_consumed).
func decodeLiteralHeader(block []byte, prefixBits int) (name, value string, n int) {
	mask := byte((1 << prefixBits) - 1)
	idx, n := decodeHpackInt(block, prefixBits)

	if idx == 0 {
		// New name literal
		s, sn := decodeHpackString(block[n:])
		name = s
		n += sn
	} else if int(idx) < len(hpackStaticTable) {
		// Indexed name from static table
		name = hpackStaticTable[idx].name
	}
	_ = mask

	value, vn := decodeHpackString(block[n:])
	n += vn
	return name, value, n
}

// decodeHpackInt decodes an HPACK integer with an N-bit prefix from block[0:].
// Returns (value, bytes_consumed).
func decodeHpackInt(block []byte, prefixBits int) (uint64, int) {
	if len(block) == 0 {
		return 0, 0
	}
	mask := byte((1 << prefixBits) - 1)
	val := uint64(block[0] & mask)
	if val < uint64(mask) {
		return val, 1
	}
	// Multi-byte integer
	shift := 0
	for i := 1; i < len(block); i++ {
		val += uint64(block[i]&0x7f) << shift
		shift += 7
		if block[i]&0x80 == 0 {
			return val, i + 1
		}
	}
	return val, len(block)
}

// decodeHpackString decodes an HPACK string literal from block[0:].
// Returns ("", n) for Huffman-encoded strings (H=1) — caller should treat as opaque.
func decodeHpackString(block []byte) (string, int) {
	if len(block) == 0 {
		return "", 0
	}
	huffman := block[0]&0x80 != 0
	length, n := decodeHpackInt(block, 7)
	if int(n)+int(length) > len(block) {
		return "", len(block) // truncated
	}
	raw := block[n : n+int(length)]
	n += int(length)
	if huffman {
		// Huffman decoding not implemented; skip and return empty.
		// Most OTEL collectors use Huffman for header names but not for
		// short values like grpc-status "0". Callers fall back on RST_STREAM
		// detection for error cases.
		return decodeHuffman(raw), n
	}
	return string(raw), n
}
