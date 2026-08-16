// Package w3c provides utilities for parsing and formatting W3C Trace Context.
// This package implements the W3C Trace Context specification (https://www.w3.org/TR/trace-context/).
//
// The W3C Trace Context format is used for distributed tracing across systems and protocols.
// While originally designed for HTTP, the format can be used with any carrier (gRPC metadata,
// message queue headers, etc.).
package w3c

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/kzs0/bedrock/internal"
)

// W3C Trace Context specification: https://www.w3.org/TR/trace-context/
//
// Traceparent format: version-trace-id-parent-id-trace-flags
// Example: 00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01
//
// Version: 2 hex characters (currently "00")
// Trace-ID: 32 hex characters (16 bytes)
// Parent-ID: 16 hex characters (8 bytes)
// Trace-flags: 2 hex characters (8 bits)

const (
	// Traceparent field lengths
	versionLen = 2
	traceIDLen = 32
	spanIDLen  = 16
	flagsLen   = 2
	fieldCount = 4
	minLength  = versionLen + 1 + traceIDLen + 1 + spanIDLen + 1 + flagsLen

	// Trace flags
	SampledFlag = 0x01

	// Tracestate limits
	MaxTracestateEntries  = 32
	MaxTracestateKeyLen   = 256
	MaxTracestateValueLen = 256
	// MaxTracestateLen is this implementation's acceptance and propagation
	// cap. It is not a protocol-wide wire maximum.
	MaxTracestateLen = 512
)

var (
	ErrInvalidTraceparent = errors.New("invalid traceparent header")
	ErrInvalidTraceID     = errors.New("invalid trace-id: must be 32 lowercase hex characters and not all zeros")
	ErrInvalidSpanID      = errors.New("invalid parent-id: must be 16 lowercase hex characters and not all zeros")
	ErrInvalidVersion     = errors.New("invalid version: must be 2 hex characters")
	ErrUnsupportedVersion = errors.New("unsupported version")
	ErrInvalidFlags       = errors.New("invalid flags: must be 2 hex characters")
	ErrInvalidTracestate  = errors.New("invalid tracestate header")
)

// Entry represents a single key-value pair in tracestate.
type Entry struct {
	Key   string
	Value string
}

// ParseTraceparent parses a W3C traceparent header value.
// Returns the trace ID, parent span ID, flags byte, and any error.
//
// Format: version-trace-id-parent-id-trace-flags
// Example: 00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01
func ParseTraceparent(value string) (internal.TraceID, internal.SpanID, byte, error) {
	var zeroTraceID internal.TraceID
	var zeroSpanID internal.SpanID

	if len(value) < minLength {
		return zeroTraceID, zeroSpanID, 0, ErrInvalidTraceparent
	}

	// Split into fields
	fields := strings.Split(value, "-")
	if len(fields) < fieldCount {
		return zeroTraceID, zeroSpanID, 0, ErrInvalidTraceparent
	}

	version := fields[0]
	traceIDHex := fields[1]
	parentIDHex := fields[2]
	flagsHex := fields[3]

	// Validate version
	if len(version) != versionLen {
		return zeroTraceID, zeroSpanID, 0, ErrInvalidVersion
	}
	if !isLowercaseHex(version) {
		return zeroTraceID, zeroSpanID, 0, ErrInvalidVersion
	}

	// Only version 00 is fully supported, but we parse future versions
	// per spec: extract trace-id, parent-id, and sampled flag
	if version != "00" && version != "ff" {
		// Future version: try to parse trace-id and parent-id
		// Spec requires we attempt to parse even if we don't understand the version
	} else if version == "ff" {
		// Version ff is forbidden
		return zeroTraceID, zeroSpanID, 0, ErrUnsupportedVersion
	}
	if version == "00" && len(fields) != fieldCount {
		return zeroTraceID, zeroSpanID, 0, ErrInvalidTraceparent
	}

	// Parse trace-id (must be 32 lowercase hex characters)
	if len(traceIDHex) != traceIDLen {
		return zeroTraceID, zeroSpanID, 0, ErrInvalidTraceID
	}
	if !isLowercaseHex(traceIDHex) {
		return zeroTraceID, zeroSpanID, 0, ErrInvalidTraceID
	}

	traceID, err := internal.TraceIDFromHex(traceIDHex)
	if err != nil {
		return zeroTraceID, zeroSpanID, 0, ErrInvalidTraceID
	}
	if traceID.IsZero() {
		return zeroTraceID, zeroSpanID, 0, ErrInvalidTraceID
	}

	// Parse parent-id (must be 16 lowercase hex characters)
	if len(parentIDHex) != spanIDLen {
		return zeroTraceID, zeroSpanID, 0, ErrInvalidSpanID
	}
	if !isLowercaseHex(parentIDHex) {
		return zeroTraceID, zeroSpanID, 0, ErrInvalidSpanID
	}

	parentID, err := internal.SpanIDFromHex(parentIDHex)
	if err != nil {
		return zeroTraceID, zeroSpanID, 0, ErrInvalidSpanID
	}
	if parentID.IsZero() {
		return zeroTraceID, zeroSpanID, 0, ErrInvalidSpanID
	}

	// Parse flags (must be 2 hex characters)
	if len(flagsHex) != flagsLen {
		return zeroTraceID, zeroSpanID, 0, ErrInvalidFlags
	}
	if !isLowercaseHex(flagsHex) {
		return zeroTraceID, zeroSpanID, 0, ErrInvalidFlags
	}

	flagsBytes, err := hex.DecodeString(flagsHex)
	if err != nil {
		return zeroTraceID, zeroSpanID, 0, ErrInvalidFlags
	}

	return traceID, parentID, flagsBytes[0], nil
}

// FormatTraceparent formats a W3C traceparent header value.
// Always uses version 00.
//
// Format: 00-{trace-id}-{span-id}-{flags}
// Example: 00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01
func FormatTraceparent(traceID internal.TraceID, spanID internal.SpanID, sampled bool) string {
	flags := byte(0)
	if sampled {
		flags |= SampledFlag
	}

	return fmt.Sprintf("00-%s-%s-%02x",
		traceID.String(),
		spanID.String(),
		flags,
	)
}

// ParseTracestate parses a W3C tracestate header value.
// Returns a list of key-value pairs.
//
// Format: key1=value1,key2=value2,...
// Maximum 32 list members; keys must be unique. This implementation accepts up
// to MaxTracestateLen bytes, the minimum capacity required for interoperability.
func ParseTracestate(value string) ([]Entry, error) {
	if value == "" {
		return nil, nil
	}
	if len(value) > MaxTracestateLen {
		return nil, fmt.Errorf("%w: header exceeds %d bytes", ErrInvalidTracestate, MaxTracestateLen)
	}

	// Split by comma
	parts := strings.Split(value, ",")
	if len(parts) > MaxTracestateEntries {
		return nil, fmt.Errorf("%w: too many entries (max %d)", ErrInvalidTracestate, MaxTracestateEntries)
	}

	entries := make([]Entry, 0, len(parts))
	seen := make(map[string]bool)

	for _, rawPart := range parts {
		part := trimOWS(rawPart)
		if part == "" {
			continue
		}

		// Split key=value
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("%w: invalid entry format", ErrInvalidTracestate)
		}

		// OWS is permitted around list members and comma separators, not
		// around '='. In particular, a leading SP is valid value data.
		key := kv[0]
		value := kv[1]

		// Validate key
		if !IsValidTracestateKey(key) {
			return nil, fmt.Errorf("%w: invalid key format", ErrInvalidTracestate)
		}

		// Validate value
		if !IsValidTracestateValue(value) {
			return nil, fmt.Errorf("%w: invalid value format", ErrInvalidTracestate)
		}

		// Duplicate keys make the entire tracestate invalid.
		if seen[key] {
			return nil, fmt.Errorf("%w: duplicate key %q", ErrInvalidTracestate, key)
		}
		seen[key] = true

		entries = append(entries, Entry{Key: key, Value: value})
	}

	return entries, nil
}

// FormatTracestate formats a W3C tracestate header value.
// Joins at most 32 valid, unique entries without exceeding this
// implementation's MaxTracestateLen propagation cap. Invalid entries and later
// duplicates are omitted because this API has no error return.
func FormatTracestate(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}

	parts := make([]string, 0, len(entries))
	totalLen := 0
	seen := make(map[string]struct{}, min(len(entries), MaxTracestateEntries))

	for _, entry := range entries {
		if len(parts) == MaxTracestateEntries {
			break
		}
		if !IsValidTracestateKey(entry.Key) || !IsValidTracestateValue(entry.Value) {
			continue
		}
		if _, duplicate := seen[entry.Key]; duplicate {
			continue
		}
		// The first valid occurrence owns the key even when it cannot be
		// propagated within the implementation limit. A later duplicate must
		// not replace it with different state.
		seen[entry.Key] = struct{}{}
		part := entry.Key + "=" + entry.Value
		nextLen := len(part)
		if len(parts) > 0 {
			nextLen++ // comma separator
		}
		if nextLen > MaxTracestateLen-totalLen {
			continue
		}
		parts = append(parts, part)
		totalLen += nextLen
	}

	return strings.Join(parts, ",")
}

// IsValidTracestateKey validates a tracestate key per W3C spec.
// A simple key starts with lowercase alpha and is at most 256 bytes. A
// multi-tenant key is {tenant}@{system}: tenant starts with lowercase alpha or
// a digit and is at most 241 bytes; system starts with lowercase alpha and is at
// most 14 bytes. Remaining bytes may be lowercase alphanumeric or _-*/.
func IsValidTracestateKey(key string) bool {
	if key == "" || len(key) > MaxTracestateKeyLen {
		return false
	}

	// Check for multi-tenant format
	if strings.Contains(key, "@") {
		if strings.Count(key, "@") != 1 {
			return false
		}
		tenant, system, _ := strings.Cut(key, "@")
		return isValidTracestateKeyPart(tenant, 241, true) &&
			isValidTracestateKeyPart(system, 14, false)
	}

	return isValidTracestateKeyPart(key, MaxTracestateKeyLen, false)
}

// IsValidTracestateValue validates a tracestate value per W3C spec.
// Must be printable ASCII (0x20-0x7E) excluding comma and equals.
func IsValidTracestateValue(value string) bool {
	if value == "" || len(value) > MaxTracestateValueLen {
		return false
	}
	if value[len(value)-1] == ' ' {
		return false
	}

	for _, c := range value {
		if c < 0x20 || c > 0x7E || c == ',' || c == '=' {
			return false
		}
	}
	return true
}

func trimOWS(value string) string {
	return strings.Trim(value, " \t")
}

// isLowercaseHex checks if a string contains only lowercase hexadecimal characters.
func isLowercaseHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func isValidTracestateKeyPart(key string, maxLen int, digitFirst bool) bool {
	if key == "" || len(key) > maxLen {
		return false
	}
	if first := key[0]; (first < 'a' || first > 'z') && (!digitFirst || first < '0' || first > '9') {
		return false
	}

	for _, c := range key[1:] {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') &&
			c != '_' && c != '-' && c != '*' && c != '/' {
			return false
		}
	}
	return true
}
