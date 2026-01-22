package w3c

import (
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
			name:    "duplicate keys (last wins)",
			header:  "vendor1=first,vendor2=value2,vendor1=last",
			wantErr: false,
			want: []Entry{
				{Key: "vendor2", Value: "value2"},
				{Key: "vendor1", Value: "last"},
			},
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

func TestValidationHelpers(t *testing.T) {
	tests := []struct {
		name  string
		input string
		fn    func(string) bool
		want  bool
	}{
		{"tracestate key valid simple", "vendor_key-1", IsValidTracestateKey, true},
		{"tracestate key valid multi-tenant", "tenant@vendor", IsValidTracestateKey, true},
		{"tracestate key invalid uppercase", "VENDOR", IsValidTracestateKey, false},
		{"tracestate key invalid empty", "", IsValidTracestateKey, false},
		{"tracestate value valid", "value123-_*", IsValidTracestateValue, true},
		{"tracestate value invalid comma", "val,ue", IsValidTracestateValue, false},
		{"tracestate value invalid equals", "val=ue", IsValidTracestateValue, false},
		{"tracestate value invalid control char", "val\x00ue", IsValidTracestateValue, false},
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
