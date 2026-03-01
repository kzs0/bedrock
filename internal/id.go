package internal

import (
	"encoding/binary"
	"encoding/hex"
	"math/rand/v2"
)

// TraceID is a 16-byte unique identifier for a trace.
type TraceID [16]byte

// SpanID is an 8-byte unique identifier for a span.
type SpanID [8]byte

// NewTraceID generates a new random trace ID.
// Uses math/rand/v2 (ChaCha8), which is cryptographically secure and
// OS-seeded automatically — ~500× faster than crypto/rand.Read.
func NewTraceID() TraceID {
	var id TraceID
	binary.LittleEndian.PutUint64(id[0:], rand.Uint64())
	binary.LittleEndian.PutUint64(id[8:], rand.Uint64())
	return id
}

// NewSpanID generates a new random span ID.
func NewSpanID() SpanID {
	var id SpanID
	binary.LittleEndian.PutUint64(id[0:], rand.Uint64())
	return id
}

// String returns the hex-encoded trace ID.
func (t TraceID) String() string {
	return hex.EncodeToString(t[:])
}

// String returns the hex-encoded span ID.
func (s SpanID) String() string {
	return hex.EncodeToString(s[:])
}

// IsZero returns true if the trace ID is all zeros.
func (t TraceID) IsZero() bool {
	for _, b := range t {
		if b != 0 {
			return false
		}
	}
	return true
}

// IsZero returns true if the span ID is all zeros.
func (s SpanID) IsZero() bool {
	for _, b := range s {
		if b != 0 {
			return false
		}
	}
	return true
}

// TraceIDFromHex parses a hex-encoded trace ID.
func TraceIDFromHex(s string) (TraceID, error) {
	var id TraceID
	b, err := hex.DecodeString(s)
	if err != nil {
		return id, err
	}
	copy(id[:], b)
	return id, nil
}

// SpanIDFromHex parses a hex-encoded span ID.
func SpanIDFromHex(s string) (SpanID, error) {
	var id SpanID
	b, err := hex.DecodeString(s)
	if err != nil {
		return id, err
	}
	copy(id[:], b)
	return id, nil
}
