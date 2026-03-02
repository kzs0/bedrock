// Package h2c implements a minimal HTTP/2 cleartext (h2c prior knowledge) client
// sufficient for making gRPC unary RPC calls. It uses only the Go standard library.
//
// Limitations:
//   - No HTTP/2 server push
//   - No multiplexing (one RPC at a time per connection)
//   - HPACK response decoding handles indexed (static table) and non-Huffman literals.
//     Huffman-encoded response header values are skipped with a warning.
//   - Connection is reused across calls; reconnected transparently on failure.
package h2c

import "encoding/binary"

// HTTP/2 frame type codes (RFC 7540 §6).
const (
	frameTypeData         = 0x0
	frameTypeHeaders      = 0x1
	frameTypeRSTStream    = 0x3
	frameTypeSettings     = 0x4
	frameTypePing         = 0x6
	frameTypeGoAway       = 0x7
	frameTypeWindowUpdate = 0x8
	frameTypeContinuation = 0x9
)

// HTTP/2 frame flags (RFC 7540 §6).
const (
	flagEndStream  = 0x1
	flagEndHeaders = 0x4
	flagAck        = 0x1
	flagPadded     = 0x8
	flagPriority   = 0x20
)

// HTTP/2 error codes (RFC 7540 §7).
const (
	errCodeNoError    = 0x0
	errCodeProtocol   = 0x1
	errCodeInternal   = 0x2
	errCodeFlowCtrl   = 0x3
	errCodeSettings   = 0x4
	errCodeRefused    = 0x8
	errCodeConnect    = 0xa
	errCodeEnhance    = 0xb
	errCodeHTTP11Req  = 0xd
)

// SETTINGS identifiers (RFC 7540 §6.5.2).
const (
	settingHeaderTableSize      = 0x1
	settingEnablePush           = 0x2
	settingMaxConcurrentStreams = 0x3
	settingInitialWindowSize    = 0x4
	settingMaxFrameSize         = 0x5
	settingMaxHeaderListSize    = 0x6
)

// Default HTTP/2 flow control window size (RFC 7540 §6.9.2).
const defaultWindowSize = 65535

// frame represents a parsed HTTP/2 frame header.
type frame struct {
	length   uint32
	typ      uint8
	flags    uint8
	streamID uint32
}

// parseFrameHeader parses the 9-byte frame header.
func parseFrameHeader(b [9]byte) frame {
	return frame{
		length:   uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2]),
		typ:      b[3],
		flags:    b[4],
		streamID: binary.BigEndian.Uint32(b[5:]) & 0x7fffffff,
	}
}

// writeFrameHeader serialises the 9-byte frame header into dst.
func writeFrameHeader(dst []byte, length uint32, typ, flags uint8, streamID uint32) {
	dst[0] = byte(length >> 16)
	dst[1] = byte(length >> 8)
	dst[2] = byte(length)
	dst[3] = typ
	dst[4] = flags
	binary.BigEndian.PutUint32(dst[5:], streamID&0x7fffffff)
}

// settingParam is a single HTTP/2 SETTINGS parameter (RFC 7540 §6.5).
type settingParam struct {
	id  uint16
	val uint32
}

// encodeSettings serialises a SETTINGS frame payload.
func encodeSettings(params []settingParam) []byte {
	b := make([]byte, len(params)*6)
	for i, p := range params {
		binary.BigEndian.PutUint16(b[i*6:], p.id)
		binary.BigEndian.PutUint32(b[i*6+2:], p.val)
	}
	return b
}

// buildFrame builds a complete HTTP/2 frame.
func buildFrame(typ, flags uint8, streamID uint32, payload []byte) []byte {
	out := make([]byte, 9+len(payload))
	writeFrameHeader(out, uint32(len(payload)), typ, flags, streamID)
	copy(out[9:], payload)
	return out
}
