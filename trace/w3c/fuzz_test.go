package w3c

import (
	"reflect"
	"strings"
	"testing"
)

func FuzzParseTraceparent(f *testing.F) {
	for _, seed := range []string{
		"",
		"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00",
		"01-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-03-extra",
		"ff-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"00-00000000000000000000000000000000-0000000000000000-00",
		"00-0AF7651916CD43DD8448EB211C80319C-b7ad6b7169203331-01",
		"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-0g",
		"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01-extra",
		"0A-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01-extra",
		"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-0A",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		// Keep fuzz executions cheap while still exceeding every valid header size.
		if len(value) > 512 {
			return
		}

		traceID, spanID, flags, err := ParseTraceparent(value)
		traceIDAgain, spanIDAgain, flagsAgain, errAgain := ParseTraceparent(value)
		if (err == nil) != (errAgain == nil) || traceID != traceIDAgain || spanID != spanIDAgain || flags != flagsAgain {
			t.Fatalf("ParseTraceparent is nondeterministic for %q", value)
		}

		if err != nil {
			if !traceID.IsZero() || !spanID.IsZero() || flags != 0 {
				t.Fatalf("failed parse returned non-zero context: trace=%s span=%s flags=%02x", traceID, spanID, flags)
			}
			return
		}
		if traceID.IsZero() || spanID.IsZero() {
			t.Fatalf("successful parse returned a zero identifier")
		}
		fields := strings.Split(value, "-")
		if fields[0] != strings.ToLower(fields[0]) || fields[3] != strings.ToLower(fields[3]) {
			t.Fatalf("successful parse accepted uppercase wire hex: %q", value)
		}
		if fields[0] == "00" && len(fields) != fieldCount {
			t.Fatalf("version 00 accepted %d fields", len(fields))
		}

		canonical := FormatTraceparent(traceID, spanID, flags&SampledFlag != 0)
		roundTraceID, roundSpanID, roundFlags, roundErr := ParseTraceparent(canonical)
		if roundErr != nil {
			t.Fatalf("canonical value %q did not parse: %v", canonical, roundErr)
		}
		if roundTraceID != traceID || roundSpanID != spanID || roundFlags&SampledFlag != flags&SampledFlag {
			t.Fatalf("canonical round trip changed context")
		}
	})
}

func FuzzParseTracestate(f *testing.F) {
	for _, seed := range []string{
		"",
		"vendor=value",
		"vendor1=value1,vendor2=value2",
		"tenant@vendor=value",
		"vendor=first,vendor=last",
		" vendor = value , second=two ",
		"missing-equals",
		"bad=comma,value",
		"bad=equals=value",
		"bad=\x00",
		",vendor=value",
		"vendor=value,",
		" \t ,\tvendor=value\t,\t",
		"\rvendor=value",
		"vendor=value\n",
		"\u00a0vendor=value",
		"key= value",
		"key =value",
		"key=value \t",
		"1vendor=value",
		tracestateAtWireLimit(),
		tracestateAtWireLimit() + "z",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 2048 {
			return
		}

		entries, err := ParseTracestate(value)
		entriesAgain, errAgain := ParseTracestate(value)
		if (err == nil) != (errAgain == nil) || !reflect.DeepEqual(entries, entriesAgain) {
			t.Fatalf("ParseTracestate is nondeterministic for %q", value)
		}
		if err != nil {
			return
		}
		if len(value) > MaxTracestateLen {
			t.Fatalf("successful parse accepted %d wire bytes", len(value))
		}
		if len(entries) > MaxTracestateEntries {
			t.Fatalf("successful parse returned %d entries", len(entries))
		}
		var wireEntries []Entry
		for _, rawMember := range strings.Split(value, ",") {
			member := trimOWS(rawMember)
			if member == "" {
				continue
			}
			key, memberValue, ok := strings.Cut(member, "=")
			if !ok {
				t.Fatalf("successful parse accepted member without equals: %q", member)
			}
			wireEntries = append(wireEntries, Entry{Key: key, Value: memberValue})
		}
		if !entriesEqual(entries, wireEntries) {
			t.Fatalf("parse normalized key/value bytes: got %#v, wire %#v", entries, wireEntries)
		}

		seen := make(map[string]struct{}, len(entries))
		for _, entry := range entries {
			if !IsValidTracestateKey(entry.Key) || !IsValidTracestateValue(entry.Value) {
				t.Fatalf("successful parse returned invalid entry %#v", entry)
			}
			if _, duplicate := seen[entry.Key]; duplicate {
				t.Fatalf("successful parse returned duplicate key %q", entry.Key)
			}
			seen[entry.Key] = struct{}{}
		}

		formatted := FormatTracestate(entries)
		if len(formatted) > MaxTracestateLen {
			t.Fatalf("formatted tracestate contains %d wire bytes", len(formatted))
		}
		roundTrip, roundErr := ParseTracestate(formatted)
		if roundErr != nil {
			t.Fatalf("formatted tracestate %q did not parse: %v", formatted, roundErr)
		}
		if !entriesEqual(roundTrip, entries) {
			t.Fatalf("tracestate round trip = %#v, want %#v", roundTrip, entries)
		}
	})
}

func FuzzFormatTracestateEntries(f *testing.F) {
	f.Add("first", "one", "second", "two")
	f.Add("first", "one", "first", "duplicate")
	f.Add("bad\nkey", "value", "safe", "value")
	f.Add("safe", "bad\rvalue", "other", "value")
	f.Add("a"+strings.Repeat("k", 255), strings.Repeat("x", 256), "next", "value")

	f.Fuzz(func(t *testing.T, key1, value1, key2, value2 string) {
		if len(key1) > 512 || len(value1) > 512 || len(key2) > 512 || len(value2) > 512 {
			return
		}
		formatted := FormatTracestate([]Entry{{Key: key1, Value: value1}, {Key: key2, Value: value2}})
		if len(formatted) > MaxTracestateLen {
			t.Fatalf("formatted tracestate contains %d bytes", len(formatted))
		}
		entries, err := ParseTracestate(formatted)
		if err != nil {
			t.Fatalf("formatter emitted invalid tracestate %q: %v", formatted, err)
		}
		firstPartLen := len(key1) + 1 + len(value1)
		secondPartLen := len(key2) + 1 + len(value2)
		if IsValidTracestateKey(key1) && IsValidTracestateValue(value1) &&
			firstPartLen > MaxTracestateLen &&
			IsValidTracestateKey(key2) && IsValidTracestateValue(value2) &&
			secondPartLen <= MaxTracestateLen {
			if key1 == key2 {
				if formatted != "" {
					t.Fatalf("later duplicate replaced oversized first valid entry: %q", formatted)
				}
			} else if formatted != key2+"="+value2 {
				t.Fatalf("safe entry after oversized entry was omitted: %q", formatted)
			}
		}
		seen := make(map[string]struct{}, len(entries))
		for _, entry := range entries {
			if !IsValidTracestateKey(entry.Key) || !IsValidTracestateValue(entry.Value) {
				t.Fatalf("formatter emitted invalid entry %#v", entry)
			}
			if _, duplicate := seen[entry.Key]; duplicate {
				t.Fatalf("formatter emitted duplicate key %q", entry.Key)
			}
			seen[entry.Key] = struct{}{}
		}
	})
}

func entriesEqual(a, b []Entry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
