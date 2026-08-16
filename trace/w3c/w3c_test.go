package w3c

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kzs0/bedrock/internal"
)

func TestParseTraceparent(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		wantErr   bool
		checkFunc func(t *testing.T, traceID internal.TraceID, spanID internal.SpanID, flags byte)
	}{
		{
			name:    "valid traceparent with sampled flag",
			header:  "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
			wantErr: false,
			checkFunc: func(t *testing.T, traceID internal.TraceID, spanID internal.SpanID, flags byte) {
				if traceID.String() != "0af7651916cd43dd8448eb211c80319c" {
					t.Errorf("expected trace ID 0af7651916cd43dd8448eb211c80319c, got %s", traceID.String())
				}
				if spanID.String() != "b7ad6b7169203331" {
					t.Errorf("expected span ID b7ad6b7169203331, got %s", spanID.String())
				}
				if flags != 0x01 {
					t.Errorf("expected flags 0x01, got 0x%02x", flags)
				}
			},
		},
		{
			name:    "valid traceparent without sampled flag",
			header:  "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00",
			wantErr: false,
			checkFunc: func(t *testing.T, traceID internal.TraceID, spanID internal.SpanID, flags byte) {
				if flags != 0x00 {
					t.Errorf("expected flags 0x00, got 0x%02x", flags)
				}
			},
		},
		{
			name:    "invalid: too short",
			header:  "00-abc-def-01",
			wantErr: true,
		},
		{
			name:    "invalid: all-zero trace ID",
			header:  "00-00000000000000000000000000000000-b7ad6b7169203331-01",
			wantErr: true,
		},
		{
			name:    "invalid: all-zero span ID",
			header:  "00-0af7651916cd43dd8448eb211c80319c-0000000000000000-01",
			wantErr: true,
		},
		{
			name:    "invalid: uppercase hex in trace ID",
			header:  "00-0AF7651916CD43DD8448EB211C80319C-b7ad6b7169203331-01",
			wantErr: true,
		},
		{
			name:    "invalid: uppercase hex in span ID",
			header:  "00-0af7651916cd43dd8448eb211c80319c-B7AD6B7169203331-01",
			wantErr: true,
		},
		{
			name:    "invalid: non-hex characters",
			header:  "00-0af7651916cd43dd8448eb211c80319z-b7ad6b7169203331-01",
			wantErr: true,
		},
		{
			name:    "invalid: version ff (forbidden)",
			header:  "ff-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
			wantErr: true,
		},
		{
			name:    "invalid: wrong field count",
			header:  "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331",
			wantErr: true,
		},
		{
			name:    "strict compatibility: version 00 rejects extension fields",
			header:  "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01-extra",
			wantErr: true,
		},
		{
			name:    "strict compatibility: uppercase version hex is invalid",
			header:  "0A-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01-extra",
			wantErr: true,
		},
		{
			name:    "strict compatibility: uppercase flags hex is invalid",
			header:  "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-0A",
			wantErr: true,
		},
		{
			name:    "future version: parse successfully",
			header:  "01-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01-extra-data",
			wantErr: false,
			checkFunc: func(t *testing.T, traceID internal.TraceID, spanID internal.SpanID, flags byte) {
				// Should still parse trace ID and span ID from future versions
				if traceID.String() != "0af7651916cd43dd8448eb211c80319c" {
					t.Errorf("expected trace ID to be parsed from future version")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			traceID, spanID, flags, err := ParseTraceparent(tt.header)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseTraceparent() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && tt.checkFunc != nil {
				tt.checkFunc(t, traceID, spanID, flags)
			}
		})
	}
}

func TestFormatTraceparent(t *testing.T) {
	traceID, _ := internal.TraceIDFromHex("0af7651916cd43dd8448eb211c80319c")
	spanID, _ := internal.SpanIDFromHex("b7ad6b7169203331")

	tests := []struct {
		name    string
		traceID internal.TraceID
		spanID  internal.SpanID
		sampled bool
		want    string
	}{
		{
			name:    "sampled",
			traceID: traceID,
			spanID:  spanID,
			sampled: true,
			want:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		},
		{
			name:    "not sampled",
			traceID: traceID,
			spanID:  spanID,
			sampled: false,
			want:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatTraceparent(tt.traceID, tt.spanID, tt.sampled)
			if got != tt.want {
				t.Errorf("FormatTraceparent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTraceparentRoundTrip(t *testing.T) {
	// Test that we can parse what we format
	traceID := internal.NewTraceID()
	spanID := internal.NewSpanID()
	sampled := true

	formatted := FormatTraceparent(traceID, spanID, sampled)
	parsedTraceID, parsedSpanID, flags, err := ParseTraceparent(formatted)

	if err != nil {
		t.Fatalf("failed to parse formatted traceparent: %v", err)
	}

	if parsedTraceID != traceID {
		t.Errorf("trace ID mismatch: got %s, want %s", parsedTraceID.String(), traceID.String())
	}

	if parsedSpanID != spanID {
		t.Errorf("span ID mismatch: got %s, want %s", parsedSpanID.String(), spanID.String())
	}

	if (flags & SampledFlag) == 0 {
		t.Errorf("sampled flag not set")
	}
}

func TestParseTracestate(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		wantErr bool
		want    []Entry
	}{
		{
			name:    "empty",
			header:  "",
			wantErr: false,
			want:    nil,
		},
		{
			name:    "single entry",
			header:  "vendor1=value1",
			wantErr: false,
			want:    []Entry{{Key: "vendor1", Value: "value1"}},
		},
		{
			name:    "multiple entries",
			header:  "vendor1=value1,vendor2=value2",
			wantErr: false,
			want: []Entry{
				{Key: "vendor1", Value: "value1"},
				{Key: "vendor2", Value: "value2"},
			},
		},
		{
			name:    "multi-tenant key",
			header:  "tenant@vendor=value",
			wantErr: false,
			want:    []Entry{{Key: "tenant@vendor", Value: "value"}},
		},
		{
			name:    "strict compatibility: duplicate keys invalidate the header",
			header:  "vendor1=first,vendor2=value2,vendor1=last",
			wantErr: true,
		},
		{
			name:    "with spaces",
			header:  "vendor1=value1, vendor2=value2",
			wantErr: false,
			want: []Entry{
				{Key: "vendor1", Value: "value1"},
				{Key: "vendor2", Value: "value2"},
			},
		},
		{
			name:    "ABNF compatibility: leading value space is preserved",
			header:  "key= value",
			wantErr: false,
			want:    []Entry{{Key: "key", Value: " value"}},
		},
		{
			name:    "strict compatibility: whitespace before equals is invalid",
			header:  "key =value",
			wantErr: true,
		},
		{
			name:    "outer trailing OWS is not value data",
			header:  "key=value \t",
			wantErr: false,
			want:    []Entry{{Key: "key", Value: "value"}},
		},
		{
			name:    "invalid: no equals sign",
			header:  "vendor1",
			wantErr: true,
		},
		{
			name:    "invalid: too many entries",
			header:  strings.Repeat("v=1,", 33) + "v=1", // 34 entries
			wantErr: true,
		},
		{
			name:    "invalid: comma in value",
			header:  "vendor=val,ue",
			wantErr: true,
		},
		{
			name:    "invalid: equals in value",
			header:  "vendor=val=ue",
			wantErr: true,
		},
		{
			name:    "ABNF compatibility: leading empty member is accepted",
			header:  ",vendor=value",
			wantErr: false,
			want:    []Entry{{Key: "vendor", Value: "value"}},
		},
		{
			name:    "ABNF compatibility: trailing empty member is accepted",
			header:  "vendor=value,",
			wantErr: false,
			want:    []Entry{{Key: "vendor", Value: "value"}},
		},
		{
			name:    "ABNF compatibility: interior empty member is accepted",
			header:  "vendor=value,,other=value",
			wantErr: false,
			want: []Entry{
				{Key: "vendor", Value: "value"},
				{Key: "other", Value: "value"},
			},
		},
		{
			name:    "ABNF compatibility: OWS-only member is accepted",
			header:  " \t ,\tvendor=value\t,\t",
			wantErr: false,
			want:    []Entry{{Key: "vendor", Value: "value"}},
		},
		{
			name:    "strict compatibility: carriage return is not OWS",
			header:  "\rvendor=value",
			wantErr: true,
		},
		{
			name:    "strict compatibility: line feed is not OWS",
			header:  "vendor=value\n",
			wantErr: true,
		},
		{
			name:    "strict compatibility: Unicode whitespace is not OWS",
			header:  "\u00a0vendor=value",
			wantErr: true,
		},
		{
			name:    "512-byte boundary",
			header:  tracestateAtWireLimit(),
			wantErr: false,
			want: []Entry{
				{Key: "a", Value: strings.Repeat("x", 256)},
				{Key: "b", Value: strings.Repeat("y", 251)},
			},
		},
		{
			name:    "strict compatibility: reject 513-byte header",
			header:  tracestateAtWireLimit() + "z",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTracestate(tt.header)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseTracestate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if len(got) != len(tt.want) {
					t.Errorf("ParseTracestate() got %d entries, want %d", len(got), len(tt.want))
					return
				}
				for i := range got {
					if got[i].Key != tt.want[i].Key || got[i].Value != tt.want[i].Value {
						t.Errorf("entry %d: got %+v, want %+v", i, got[i], tt.want[i])
					}
				}
			}
		})
	}
}

func TestFormatTracestate(t *testing.T) {
	tests := []struct {
		name    string
		entries []Entry
		want    string
	}{
		{
			name:    "empty",
			entries: nil,
			want:    "",
		},
		{
			name:    "single entry",
			entries: []Entry{{Key: "vendor1", Value: "value1"}},
			want:    "vendor1=value1",
		},
		{
			name: "multiple entries",
			entries: []Entry{
				{Key: "vendor1", Value: "value1"},
				{Key: "vendor2", Value: "value2"},
			},
			want: "vendor1=value1,vendor2=value2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatTracestate(tt.entries)
			if got != tt.want {
				t.Errorf("FormatTracestate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatTracestateStrictWireLimits(t *testing.T) {
	exactEntries := []Entry{
		{Key: "a", Value: strings.Repeat("x", 256)},
		{Key: "b", Value: strings.Repeat("y", 251)},
	}
	if got := FormatTracestate(exactEntries); got != tracestateAtWireLimit() || len(got) != MaxTracestateLen {
		t.Fatalf("exact-limit format length = %d, want %d", len(got), MaxTracestateLen)
	}

	withOverflow := append(append([]Entry(nil), exactEntries...), Entry{Key: "c", Value: "z"})
	if got := FormatTracestate(withOverflow); got != tracestateAtWireLimit() {
		t.Fatalf("format appended entry beyond limit: length %d", len(got))
	}
	oversizedPart := Entry{Key: "a" + strings.Repeat("k", 255), Value: strings.Repeat("x", 256)}
	if got := FormatTracestate([]Entry{oversizedPart}); got != "" {
		t.Fatalf("oversized first entry formatted as %q", got)
	}
	if got := FormatTracestate([]Entry{oversizedPart, {Key: "safe", Value: "value"}}); got != "safe=value" {
		t.Fatalf("entry after oversized first entry was not considered: got %q", got)
	}
	if got := FormatTracestate([]Entry{
		oversizedPart,
		{Key: oversizedPart.Key, Value: "replacement"},
		{Key: "safe", Value: "value"},
	}); got != "safe=value" {
		t.Fatalf("later duplicate replaced oversized first valid entry: got %q", got)
	}

	many := make([]Entry, 33)
	for i := range many {
		many[i] = Entry{Key: fmt.Sprintf("key%d", i), Value: "v"}
	}
	formatted := FormatTracestate(many)
	parsed, err := ParseTracestate(formatted)
	if err != nil {
		t.Fatalf("formatted 32-entry prefix is invalid: %v", err)
	}
	if len(parsed) != MaxTracestateEntries {
		t.Fatalf("formatted entry count = %d, want %d", len(parsed), MaxTracestateEntries)
	}
}

func TestFormatTracestateFiltersInvalidAndDuplicateEntries(t *testing.T) {
	entries := []Entry{
		{Key: "first", Value: "one"},
		{Key: "bad\nkey", Value: "injected"},
		{Key: "badvalue", Value: "line\rbreak"},
		{Key: "first", Value: "duplicate"},
		{Key: "second", Value: "two"},
		{Key: "trailing", Value: "space "},
	}
	got := FormatTracestate(entries)
	if got != "first=one,second=two" {
		t.Fatalf("FormatTracestate = %q, want filtered valid entries", got)
	}
	if strings.ContainsAny(got, "\r\n\x00") {
		t.Fatalf("FormatTracestate emitted a carrier control: %q", got)
	}
	if _, err := ParseTracestate(got); err != nil {
		t.Fatalf("FormatTracestate produced invalid output: %v", err)
	}
}

func TestValidationHelpers(t *testing.T) {
	tests := []struct {
		name  string
		input string
		fn    func(string) bool
		want  bool
	}{
		{"tracestate key valid simple", "vendor_key-1", IsValidTracestateKey, true},
		{"tracestate key valid multi-tenant", "tenant@vendor", IsValidTracestateKey, true},
		{"tracestate key valid digit-leading tenant", "1tenant@vendor", IsValidTracestateKey, true},
		{"tracestate key valid maximum simple length", "a" + strings.Repeat("1", 255), IsValidTracestateKey, true},
		{"tracestate key valid maximum tenant and system lengths", "1" + strings.Repeat("a", 240) + "@" + strings.Repeat("b", 14), IsValidTracestateKey, true},
		{"tracestate key invalid uppercase", "VENDOR", IsValidTracestateKey, false},
		{"tracestate key invalid empty", "", IsValidTracestateKey, false},
		{"strict compatibility: simple key cannot start with digit", "1vendor", IsValidTracestateKey, false},
		{"strict compatibility: simple key cannot start with symbol", "_vendor", IsValidTracestateKey, false},
		{"strict compatibility: system id cannot start with digit", "tenant@1vendor", IsValidTracestateKey, false},
		{"strict compatibility: tenant id cannot start with symbol", "_tenant@vendor", IsValidTracestateKey, false},
		{"tracestate key invalid overlong simple key", "a" + strings.Repeat("1", 256), IsValidTracestateKey, false},
		{"tracestate key invalid overlong tenant id", "1" + strings.Repeat("a", 241) + "@vendor", IsValidTracestateKey, false},
		{"tracestate key invalid overlong system id", "tenant@" + strings.Repeat("b", 15), IsValidTracestateKey, false},
		{"tracestate value valid", "value123-_*", IsValidTracestateValue, true},
		{"tracestate value valid with leading space", " value", IsValidTracestateValue, true},
		{"tracestate value invalid comma", "val,ue", IsValidTracestateValue, false},
		{"tracestate value invalid equals", "val=ue", IsValidTracestateValue, false},
		{"tracestate value invalid control char", "val\x00ue", IsValidTracestateValue, false},
		{"tracestate value invalid trailing space", "value ", IsValidTracestateValue, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fn(tt.input)
			if got != tt.want {
				t.Errorf("%s(%q) = %v, want %v", tt.name, tt.input, got, tt.want)
			}
		})
	}
}

func tracestateAtWireLimit() string {
	return "a=" + strings.Repeat("x", 256) + ",b=" + strings.Repeat("y", 251)
}
