package e2e

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pprof "github.com/google/pprof/profile"
	collector "go.opentelemetry.io/proto/otlp/collector/profiles/v1development"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"
)

func TestPrivacyContractTypedGraphsAndCustomPolicy(t *testing.T) {
	traces := newReceiver(t)
	app := startApp(t, fixtureConfig{Scenario: "privacy", TraceEndpoint: traces.URL + "/v1/traces"})
	var result map[string]any
	if err := json.Unmarshal(profileGET(t, app.URL+"/policy", 200), &result); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"visible": "safe-marker", "password": "[redacted]", "custom": "custom-redacted", "pointer": "[unsupported]", "function": "[unsupported]", "channel": "[unsupported]", "struct": "[unsupported]", "unsupported": "[unsupported]", "broad": "[truncated]", "boolean": true, "integer": float64(17), "utf8": "a�b", "long": "abcdefghijklmnopqrstuvwx[truncated]", "http.url": "https://example.test", "error.message": "reviewed-error", "code.filepath": "/reviewed/source.go"} {
		if !reflect.DeepEqual(result[key], want) {
			t.Errorf("%s=%#v, want %#v", key, result[key], want)
		}
	}
	if _, exists := result["removed"]; exists {
		t.Fatal("callback-rejected value retained")
	}
	if !reflect.DeepEqual(result["cycle"], map[string]any{"self": "[cycle]"}) {
		t.Fatalf("cycle result: %#v", result["cycle"])
	}
	shared := result["shared"].([]any)
	if shared[1] != "[shared]" {
		t.Fatalf("shared graph: %#v", shared)
	}
	if wide := result["wide"].([]any); len(wide) != 64 || wide[63] != "[truncated]" {
		t.Fatalf("wide bound: %#v", wide)
	}
	text, _ := json.Marshal(result)
	for _, secret := range []string{"secret-never-visible", "secret-nested", "secret-map", "secret-struct", "replace-this", "drop-this", "deep-marker", "user:pass", "private?"} {
		if bytes.Contains(text, []byte(secret)) {
			t.Errorf("policy leaked %q", secret)
		}
	}
	result = map[string]any{}
	if err := json.Unmarshal(profileGET(t, app.URL+"/policy?allow=selected", 200), &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 || !reflect.DeepEqual(result["nested"], map[string]any{"safe": "nested-safe"}) {
		t.Fatalf("nested allow list: %#v", result["nested"])
	}
	var empty map[string]any
	if err := json.Unmarshal(profileGET(t, app.URL+"/policy?allow=empty", 200), &empty); err != nil || len(empty) != 0 {
		t.Fatalf("empty allow list: %#v, %v", empty, err)
	}
	app.Stop(t)
	var found bool
	for {
		select {
		case request := <-traces.Requests:
			var resources struct {
				ResourceSpans []struct {
					Resource struct {
						Attributes []struct {
							Key   string
							Value struct{ StringValue string }
						}
					}
				}
			}
			if err := json.Unmarshal(request.Body, &resources); err != nil {
				t.Fatal(err)
			}
			for _, resource := range resources.ResourceSpans {
				attrs := map[string]string{}
				for _, a := range resource.Resource.Attributes {
					attrs[a.Key] = a.Value.StringValue
				}
				if attrs["host.name"] != "explicit-host-marker" || attrs["service.namespace"] != "safe-resource-marker" {
					t.Fatalf("resource opt-in: %v", attrs)
				}
			}
			for _, span := range behaviorSpans(t, request) {
				if span.Name == "privacy.contract" {
					found = true
					if behaviorStringAttr(span, "password") != "[redacted]" || behaviorStringAttr(span, "custom") != "custom-redacted" {
						t.Fatalf("exported policy: %v", span)
					}
				}
			}

		default:
			if !found {
				t.Fatal("positive privacy span missing")
			}
			logs := app.Logs()
			if !strings.Contains(logs, "privacy.contract.log") || !strings.Contains(logs, "safe-marker") || strings.Contains(logs, "secret-log") {
				t.Fatalf("grouped log privacy: %s", logs)
			}
			return
		}
	}
}

func TestPrivacyContractCPUCollectionCancellation(t *testing.T) {
	receiver := newReceiver(t)
	app := startApp(t, fixtureConfig{Scenario: "privacy", ProfileEndpoint: receiver.URL + "/v1/profiles"})
	response, err := (&http.Client{Timeout: 9 * time.Second}).Get(app.URL + "/profile-cancel")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var result struct {
		Owned, Released, Bounded bool
		Stats                    resilienceProfileStats
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !result.Owned || !result.Released || !result.Bounded || result.Stats.Exported != 0 || result.Stats.Failed != 1 {
		t.Fatalf("CPU collection cancellation: %+v, status %d", result, response.StatusCode)
	}
	app.Stop(t)
	if stats := app.FinalStats(t); stats.Profiles.Exported != 0 || stats.Profiles.Failed != 1 {
		t.Fatalf("CPU cancellation final stats: %+v", stats)
	}
	assertResilienceNoExport(t, receiver)
}

func TestPrivacyContractProfileUploadDisconnectRecovery(t *testing.T) {
	receiver := newReceiver(t)
	var attempts atomic.Uint64
	receiver.SetHandler(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = connection.Close()
			}
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	app := startApp(t, fixtureConfig{ProfileEndpoint: receiver.URL + "/v1/profiles", ProfileTypes: []string{"goroutine"}, ProfileIntervalMillis: 2000, ProfileRetries: 1})
	first := nextBehaviorExport(t, receiver.Requests)
	second := nextBehaviorExport(t, receiver.Requests)
	assertResilienceProfile(t, first, "goroutine")
	assertResilienceProfile(t, second, "goroutine")
	if !bytes.Equal(first.Body, second.Body) {
		t.Fatal("network retry changed native profile identity")
	}
	awaitResilienceStats(t, app, func(stats resilienceProfileStats) bool { return stats.Exported == 1 })
	app.Stop(t)
	assertResilienceFinalStats(t, app, resilienceProfileStats{Exported: 1})
	if attempts.Load() != 2 {
		t.Fatalf("network retry attempts=%d, want 2", attempts.Load())
	}
	assertResilienceNoExport(t, receiver)
}

func contractRawProfile(t *testing.T, labels map[string][]string, numbers map[string][]int64, units map[string][]string) []byte {
	t.Helper()
	mapping := &pprof.Mapping{ID: 1, Start: 100, Limit: 200, File: "/private/application/bin"}
	function := &pprof.Function{ID: 1, Name: "contract.workload", SystemName: "contract.workload", Filename: "/private/application/source.go", StartLine: 10}
	location := &pprof.Location{ID: 1, Mapping: mapping, Address: 123, Line: []pprof.Line{{Function: function, Line: 11}}}
	p := &pprof.Profile{SampleType: []*pprof.ValueType{{Type: "objects", Unit: "count"}, {Type: "space", Unit: "bytes"}}, Sample: []*pprof.Sample{{Location: []*pprof.Location{location}, Value: []int64{0, -7}, Label: labels, NumLabel: numbers, NumUnit: units}}, Mapping: []*pprof.Mapping{mapping}, Location: []*pprof.Location{location}, Function: []*pprof.Function{function}, PeriodType: &pprof.ValueType{Type: "cpu", Unit: "nanoseconds"}, Period: 100, TimeNanos: 123, DurationNanos: 1000}
	var raw bytes.Buffer
	if err := p.WriteUncompressed(&raw); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func contractProfilePost(t *testing.T, app *fixtureApp, query string, raw []byte, want int) []byte {
	t.Helper()
	response, err := (&http.Client{Timeout: 10 * time.Second}).Post(app.URL+"/encode-profile?"+query, "application/octet-stream", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("profile status=%d want=%d: %s", response.StatusCode, want, body)
	}
	return body
}

func contractDecodeProfile(t *testing.T, body []byte) *collector.ExportProfilesServiceRequest {
	t.Helper()
	var decoded collector.ExportProfilesServiceRequest
	if err := proto.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.ResourceProfiles) != 1 || len(decoded.ResourceProfiles[0].ScopeProfiles) != 1 {
		t.Fatalf("profile envelope: %v", &decoded)
	}
	return &decoded
}

func contractAnyValue(value *common.AnyValue) any {
	switch v := value.Value.(type) {
	case *common.AnyValue_StringValue:
		return v.StringValue
	case *common.AnyValue_IntValue:
		return v.IntValue
	case *common.AnyValue_DoubleValue:
		return v.DoubleValue
	case *common.AnyValue_BoolValue:
		return v.BoolValue
	case *common.AnyValue_ArrayValue:
		out := make([]any, 0, len(v.ArrayValue.Values))
		for _, item := range v.ArrayValue.Values {
			out = append(out, contractAnyValue(item))
		}
		return out
	case *common.AnyValue_KvlistValue:
		out := map[string]any{}
		for _, item := range v.KvlistValue.Values {
			out[item.Key] = contractAnyValue(item.Value)
		}
		return out
	}
	return nil
}

func TestPrivacyContractProfileTypedLabelsAndPaths(t *testing.T) {
	app := startApp(t, fixtureConfig{Scenario: "privacy"})
	labels := map[string][]string{}
	for _, key := range []string{"bool", "false", "float", "int", "uint", "uint_max", "uint_intmax", "nil", "map", "array", "plain"} {
		labels[key] = []string{"native-label-marker"}
	}
	raw := contractRawProfile(t, labels, map[string][]int64{"count": {0, 12}, "bytes": {64}}, map[string][]string{"count": {"count", "count"}, "bytes": {"bytes"}})
	for _, fullPaths := range []bool{false, true} {
		t.Run(fmt.Sprint(fullPaths), func(t *testing.T) {
			query := ""
			if fullPaths {
				query = "paths=full"
			}
			decoded := contractDecodeProfile(t, contractProfilePost(t, app, query, raw, 200))
			profiles := decoded.ResourceProfiles[0].ScopeProfiles[0].Profiles
			if len(profiles) != 2 {
				t.Fatalf("sample types: %d", len(profiles))
			}
			for i, profile := range profiles {
				want := int64(0)
				if i == 1 {
					want = -7
				}
				if len(profile.Samples) != 1 || len(profile.Samples[0].Values) != 1 || profile.Samples[0].Values[0] != want {
					t.Fatalf("native values: %v", profile.Samples)
				}
				if profile.DurationNano != uint64(time.Second) {
					t.Fatalf("duration: %d", profile.DurationNano)
				}
			}
			attributes := map[string]any{}
			units := map[string]string{}
			for _, index := range profiles[0].Samples[0].AttributeIndices {
				a := decoded.Dictionary.AttributeTable[index]
				key := profileString(t, decoded.Dictionary, a.KeyStrindex)
				attributes[key] = contractAnyValue(a.Value)
				units[key] = profileString(t, decoded.Dictionary, a.UnitStrindex)
			}
			for key, want := range map[string]any{"bool": true, "false": false, "float": 1.25, "int": int64(-5), "uint": "9223372036854775808", "uint_max": "18446744073709551615", "uint_intmax": int64(1<<63 - 1), "nil": nil, "plain": "native-label-marker", "array": []any{int64(1), "array-marker", false}, "map": map[string]any{"n": int64(3), "s": "map-marker", "b": true}, "count": []any{int64(0), int64(12)}, "bytes": int64(64)} {
				if !reflect.DeepEqual(attributes[key], want) {
					t.Errorf("label %s=%#v want %#v", key, attributes[key], want)
				}
			}
			if units["count"] != "count" || units["bytes"] != "bytes" {
				t.Fatalf("numeric units: %v", units)
			}
			stringsTable := strings.Join(decoded.Dictionary.StringTable, "\n")
			if strings.Contains(stringsTable, "/private/application/") != fullPaths {
				t.Fatalf("source path policy=%t: %s", fullPaths, stringsTable)
			}
		})
	}
	app.Stop(t)
}

func TestPrivacyContractProfileMergingAndDisabledLabels(t *testing.T) {
	app := startApp(t, fixtureConfig{Scenario: "privacy"})
	raw := contractRawProfile(t, map[string][]string{"a": {"alpha"}, "b": {"beta"}}, nil, nil)
	for _, mode := range []string{"none", "drop", "merge", "merge-array"} {
		t.Run(mode, func(t *testing.T) {
			decoded := contractDecodeProfile(t, contractProfilePost(t, app, "filter="+mode+"&reverse=true", raw, 200))
			profile := decoded.ResourceProfiles[0].ScopeProfiles[0].Profiles[0]
			if profile.DurationNano != 0 {
				t.Fatal("negative duration retained")
			}
			if mode == "none" || mode == "drop" {
				if len(profile.Samples[0].AttributeIndices) != 0 {
					t.Fatal("disabled labels retained")
				}
				return
			}
			if len(profile.Samples[0].AttributeIndices) != 1 {
				t.Fatal("renamed labels not merged")
			}
			a := decoded.Dictionary.AttributeTable[profile.Samples[0].AttributeIndices[0]]
			want := []any{"alpha", "beta"}
			if mode == "merge-array" {
				want = []any{int64(1), int64(2), int64(1), int64(2)}
			}
			if profileString(t, decoded.Dictionary, a.KeyStrindex) != "merged" || !reflect.DeepEqual(contractAnyValue(a.Value), want) {
				t.Fatalf("merged label: %v", a)
			}
		})
	}
	mixed := contractRawProfile(t, nil, map[string][]int64{"a": {1, 2}}, map[string][]string{"a": {"bytes", "count"}})
	decoded := contractDecodeProfile(t, contractProfilePost(t, app, "", mixed, 200))
	profile := decoded.ResourceProfiles[0].ScopeProfiles[0].Profiles[0]
	a := decoded.Dictionary.AttributeTable[profile.Samples[0].AttributeIndices[0]]
	want := []any{map[string]any{"value": int64(1), "unit": "bytes"}, map[string]any{"value": int64(2), "unit": "count"}}
	if a.UnitStrindex != 0 || !reflect.DeepEqual(contractAnyValue(a.Value), want) {
		t.Fatalf("mixed numeric units: %v", a)
	}
	app.Stop(t)
}

func contractGzip(t *testing.T, raw []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestPrivacyContractProfileInputAndAttributeBounds(t *testing.T) {
	app := startApp(t, fixtureConfig{Scenario: "privacy"})
	for _, key := range []string{"array65", "map65", "badmap", "deep", "duration", "time", "error", "nodes"} {
		t.Run(key, func(t *testing.T) {
			raw := contractRawProfile(t, map[string][]string{key: {"value"}}, nil, nil)
			body := contractProfilePost(t, app, "", raw, 400)
			if !bytes.Contains(body, []byte("otlp/profiles:")) {
				t.Fatalf("missing bounded rejection: %s", body)
			}
		})
	}
	labels := map[string][]string{}
	for i := range 65 {
		labels[fmt.Sprintf("label%02d", i)] = []string{"value"}
	}
	contractProfilePost(t, app, "", contractRawProfile(t, labels, nil, nil), 400)
	delete(labels, "label64")
	accepted := contractDecodeProfile(t, contractProfilePost(t, app, "", contractRawProfile(t, labels, nil, nil), 200))
	if indices := accepted.ResourceProfiles[0].ScopeProfiles[0].Profiles[0].Samples[0].AttributeIndices; len(indices) != 64 {
		t.Fatalf("accepted label boundary: %d sample attributes", len(indices))
	}
	valid := contractRawProfile(t, map[string][]string{"plain": {"visible"}}, nil, nil)
	contractDecodeProfile(t, contractProfilePost(t, app, "", contractGzip(t, valid), 200))
	for _, bad := range [][]byte{nil, {0xff}, {0x1f, 0x8b, 0}, contractGzip(t, contractGzip(t, valid)), contractGzip(t, bytes.Repeat([]byte{0}, (32<<20)+1)), bytes.Repeat([]byte{0}, (32<<20)+1)} {
		contractProfilePost(t, app, "", bad, 400)
	}
	app.Stop(t)
}

func TestPrivacyContractProfileConfigurationRejection(t *testing.T) {
	app := startApp(t, fixtureConfig{Scenario: "privacy"})
	for _, kind := range []string{"scheme", "plain", "credentials", "query", "fragment", "timeout", "interval", "window", "bytes", "oversize", "retries", "negative-retries", "type", "cpu", "mutex", "block", "tls", "disabled", "valid"} {
		t.Run(kind, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"Kind": kind})
			response, err := http.Post(app.URL+"/profile-config", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			want := 400
			if kind == "valid" || kind == "disabled" {
				want = 204
			}
			if response.StatusCode != want {
				raw, _ := io.ReadAll(response.Body)
				t.Fatalf("config %s=%d want%d: %s", kind, response.StatusCode, want, raw)
			}
		})
	}
	app.Stop(t)
	stats := app.FinalStats(t)
	if stats.Profiles.Exported != 0 || stats.Profiles.Failed != 0 || stats.Profiles.Skipped != 0 {
		t.Fatalf("pre-tick config effects: %+v", stats)
	}
}

func TestPrivacyContractProfileLifecycleAndDisabledSamplers(t *testing.T) {
	for _, path := range []string{"/profile-lifecycle", "/profile-skip"} {
		t.Run(path, func(t *testing.T) {
			receiver := newReceiver(t)
			app := startApp(t, fixtureConfig{Scenario: "privacy", ProfileEndpoint: receiver.URL + "/v1/profiles"})
			body := profileGET(t, app.URL+path, http.StatusOK)
			if path == "/profile-lifecycle" {
				var result struct {
					MutexConflict            bool `json:"mutex_conflict"`
					Admitted                 int
					Capped, Closed, Shutdown bool
				}
				if err := json.Unmarshal(body, &result); err != nil || !result.MutexConflict || result.Admitted != 8 || !result.Capped || !result.Closed || !result.Shutdown {
					t.Fatalf("profile lifecycle: %+v, %v", result, err)
				}
			} else {
				var result resilienceProfileStats
				if err := json.Unmarshal(body, &result); err != nil || result.Skipped < 2 || result.Failed != 0 || result.Exported != 0 {
					t.Fatalf("disabled samplers: %+v, %v", result, err)
				}
			}
			app.Stop(t)
			assertResilienceNoExport(t, receiver)
		})
	}
}
