package otlp_test

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace/otlp"
	collector "go.opentelemetry.io/proto/otlp/collector/profiles/v1development"
	profiles "go.opentelemetry.io/proto/otlp/profiles/v1development"
	"google.golang.org/protobuf/proto"
)

func TestProfileEncodingPreservesNativeSamples(t *testing.T) {
	start := time.Unix(1700000000, 0)
	end := start.Add(time.Second)
	encoded := otlp.EncodeProfile("fixture", attr.EmptySet, "cpu", fixturePprof(t, start, end), start, end)
	req := new(collector.ExportProfilesServiceRequest)
	if err := proto.Unmarshal(encoded, req); err != nil {
		t.Fatalf("official decoder rejected payload: %v", err)
	}
	verifyProfiles(t, req, start, end)
}
func TestProfilePayloadAcceptedBySchemaCheckingReceiver(t *testing.T) {
	start := time.Unix(1700000000, 0)
	end := start.Add(time.Second)
	received := make(chan *collector.ExportProfilesServiceRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		req := new(collector.ExportProfilesServiceRequest)
		if err := proto.Unmarshal(raw, req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		received <- req
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	encoded := otlp.EncodeProfile("fixture", attr.EmptySet, "cpu", fixturePprof(t, start, end), start, end)
	resp, err := http.Post(srv.URL, "application/x-protobuf", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("receiver rejected export: %d %s", resp.StatusCode, body)
	}
	verifyProfiles(t, <-received, start, end)
}
func verifyProfiles(t *testing.T, req *collector.ExportProfilesServiceRequest, start, end time.Time) {
	t.Helper()
	dict := req.GetDictionary()
	if dict == nil {
		t.Fatal("missing native profile dictionary")
	}
	if len(dict.StringTable) == 0 || dict.StringTable[0] != "" {
		t.Fatal("missing null string entry")
	}
	all := make([]*profiles.Profile, 0)
	for _, resource := range req.ResourceProfiles {
		for _, scope := range resource.ScopeProfiles {
			all = append(all, scope.Profiles...)
		}
	}
	if len(all) != 2 {
		t.Fatalf("converted profile count=%d, want two sample types", len(all))
	}
	values := make(map[string]int64)
	for _, p := range all {
		if len(p.ProfileId) != 16 {
			t.Error("missing 16-byte profile ID")
		}
		if p.TimeUnixNano != uint64(start.UnixNano()) || p.DurationNano != uint64(end.Sub(start)) {
			t.Errorf("wrong time range: %d/%d", p.TimeUnixNano, p.DurationNano)
		}
		if len(p.OriginalPayload) != 0 || p.OriginalPayloadFormat != "" {
			t.Error("raw profile retained by default")
		}
		if p.SampleType == nil {
			t.Fatal("missing sample type")
		}
		typ := p.SampleType.TypeStrindex
		if typ < 0 || int(typ) >= len(dict.StringTable) {
			t.Fatal("invalid sample type string index")
		}
		if len(p.Samples) != 1 {
			t.Fatalf("native samples=%d, want 1", len(p.Samples))
		}
		sample := p.Samples[0]
		if len(sample.Values) != 1 {
			t.Fatalf("values=%v", sample.Values)
		}
		values[dict.StringTable[typ]] = sample.Values[0]
		index := sample.StackIndex
		if index <= 0 || int(index) >= len(dict.StackTable) {
			t.Fatal("sample has invalid stack index")
		}
		stack := dict.StackTable[index]
		if len(stack.LocationIndices) != 1 {
			t.Fatal("stack lost location")
		}
		locationIndex := stack.LocationIndices[0]
		if locationIndex <= 0 || int(locationIndex) >= len(dict.LocationTable) {
			t.Fatal("invalid location index")
		}
		location := dict.LocationTable[locationIndex]
		if len(location.Lines) != 1 {
			t.Fatal("location lost source line")
		}
		functionIndex := location.Lines[0].FunctionIndex
		if functionIndex <= 0 || int(functionIndex) >= len(dict.FunctionTable) {
			t.Fatal("invalid function index")
		}
		fn := dict.FunctionTable[functionIndex]
		if int(fn.NameStrindex) >= len(dict.StringTable) || dict.StringTable[fn.NameStrindex] != "fixtureFunction" {
			t.Fatal("function name lost")
		}
	}
	if values["samples"] != 3 || values["cpu"] != 900 {
		t.Fatalf("sample values changed: %v", values)
	}
}
func fixturePprof(t *testing.T, start, end time.Time) []byte {
	t.Helper()
	var raw []byte
	for _, pair := range [][2]uint64{{1, 2}, {3, 4}} {
		var v []byte
		v = fieldVarint(v, 1, pair[0])
		v = fieldVarint(v, 2, pair[1])
		raw = fieldBytes(raw, 1, v)
	}
	var sample []byte
	sample = fieldVarint(sample, 1, 1)
	sample = fieldVarint(sample, 2, 3)
	sample = fieldVarint(sample, 2, 900)
	raw = fieldBytes(raw, 2, sample)
	var line []byte
	line = fieldVarint(line, 1, 1)
	line = fieldVarint(line, 2, 42)
	var location []byte
	location = fieldVarint(location, 1, 1)
	location = fieldBytes(location, 4, line)
	raw = fieldBytes(raw, 4, location)
	var function []byte
	function = fieldVarint(function, 1, 1)
	function = fieldVarint(function, 2, 5)
	function = fieldVarint(function, 3, 5)
	function = fieldVarint(function, 4, 6)
	raw = fieldBytes(raw, 5, function)
	for _, s := range []string{"", "samples", "count", "cpu", "nanoseconds", "fixtureFunction", "/private/source.go"} {
		raw = fieldBytes(raw, 6, []byte(s))
	}
	raw = fieldVarint(raw, 9, uint64(start.UnixNano()))
	raw = fieldVarint(raw, 10, uint64(end.Sub(start)))
	var compressed bytes.Buffer
	w := gzip.NewWriter(&compressed)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}
func fieldVarint(dst []byte, field, value uint64) []byte {
	dst = binary.AppendUvarint(dst, field<<3)
	return binary.AppendUvarint(dst, value)
}
func fieldBytes(dst []byte, field uint64, value []byte) []byte {
	dst = binary.AppendUvarint(dst, field<<3|2)
	dst = binary.AppendUvarint(dst, uint64(len(value)))
	return append(dst, value...)
}
