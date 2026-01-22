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
	if !isHex(version) {
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
	if !isHex(flagsHex) {
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
// Maximum 32 entries, keys must be unique (last occurrence wins).
func ParseTracestate(value string) ([]Entry, error) {
	if value == "" {
		return nil, nil
	}

	// Split by comma
	parts := strings.Split(value, ",")
	if len(parts) > MaxTracestateEntries {
		return nil, fmt.Errorf("%w: too many entries (max %d)", ErrInvalidTracestate, MaxTracestateEntries)
	}

	entries := make([]Entry, 0, len(parts))
	seen := make(map[string]bool)

	// Process in reverse to implement "last wins" for duplicates
	for i := len(parts) - 1; i >= 0; i-- {
		part := strings.TrimSpace(parts[i])
		if part == "" {
			continue
		}

		// Split key=value
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("%w: invalid entry format", ErrInvalidTracestate)
		}

		key := strings.TrimSpace(kv[0])
		value := strings.TrimSpace(kv[1])

		// Validate key
		if !IsValidTracestateKey(key) {
			return nil, fmt.Errorf("%w: invalid key format", ErrInvalidTracestate)
		}

		// Validate value
		if !IsValidTracestateValue(value) {
			return nil, fmt.Errorf("%w: invalid value format", ErrInvalidTracestate)
		}

		// Check for duplicates (skip if already seen)
		if seen[key] {
			continue
		}
		seen[key] = true

		// Prepend to maintain order (since we're processing in reverse)
		entries = append([]Entry{{Key: key, Value: value}}, entries...)
	}

	return entries, nil
}

// FormatTracestate formats a W3C tracestate header value.
// Joins entries with commas, up to 512 characters minimum (per spec).
func FormatTracestate(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}

	parts := make([]string, 0, len(entries))
	totalLen := 0

	for _, entry := range entries {
		part := entry.Key + "=" + entry.Value
		// Ensure we propagate at least 512 characters
		if totalLen+len(part) > 512 && totalLen >= 512 {
			break
		}
		parts = append(parts, part)
		totalLen += len(part) + 1 // +1 for comma
	}

	return strings.Join(parts, ",")
}

// IsValidTracestateKey validates a tracestate key per W3C spec.
// Simple key: lowercase alphanumeric, underscore, hyphen, asterisk, slash
// Multi-tenant key: {tenant}@{system} where both parts follow simple key rules
func IsValidTracestateKey(key string) bool {
	if key == "" || len(key) > MaxTracestateKeyLen {
		return false
	}

	// Check for multi-tenant format
	if strings.Contains(key, "@") {
		parts := strings.Split(key, "@")
		if len(parts) != 2 {
			return false
		}
		return isValidSimpleKey(parts[0]) && isValidSimpleKey(parts[1])
	}

	return isValidSimpleKey(key)
}

// IsValidTracestateValue validates a tracestate value per W3C spec.
// Must be printable ASCII (0x20-0x7E) excluding comma and equals.
func IsValidTracestateValue(value string) bool {
	if value == "" || len(value) > MaxTracestateValueLen {
		return false
	}

	for _, c := range value {
		if c < 0x20 || c > 0x7E || c == ',' || c == '=' {
			return false
		}
	}
	return true
}

// isHex checks if a string contains only hexadecimal characters (case-insensitive).
func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
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

// isValidSimpleKey validates a simple tracestate key.
func isValidSimpleKey(key string) bool {
	if key == "" {
		return false
	}

	for _, c := range key {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') &&
			c != '_' && c != '-' && c != '*' && c != '/' {
			return false
		}
	}
	return true
}
