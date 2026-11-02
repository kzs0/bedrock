package e2e

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	collector "go.opentelemetry.io/proto/otlp/collector/profiles/v1development"
	profiles "go.opentelemetry.io/proto/otlp/profiles/v1development"
	"google.golang.org/protobuf/proto"
)

func TestProfileExportNativeWorkloads(t *testing.T) {
	receiver := newReceiver(t)
	app := startApp(t, fixtureConfig{
		ProfileEndpoint: receiver.URL + "/v1/profiles",
		ProfileTypes:    []string{"cpu", "heap", "goroutine", "mutex", "block"},
		CPUWindowMillis: 1000, ProfileIntervalMillis: 1600,
		ContentionSampling: true,
	})
	profileGET(t, app.ControlURL+"/workload", http.StatusNoContent)
	seen := map[string]bool{}
	sampleTypes := map[string]map[string]bool{}
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for len(seen) < 5 {
		select {
		case export := <-receiver.Requests:
			request := decodeProfileExport(t, export)
			for _, resource := range request.ResourceProfiles {
				for _, scope := range resource.ScopeProfiles {
					for _, profile := range scope.Profiles {
						kind := profileKind(t, request.Dictionary, profile)
						functions := validateNativeProfile(t, request.Dictionary, profile, kind)
						known := map[string]string{"cpu": "fixtureCPU", "heap": "startWorkload", "goroutine": "fixtureGoroutine", "mutex": "fixtureContention", "block": "fixtureContention"}[kind]
						if known == "" {
							t.Fatalf("unexpected profile type %q", kind)
						}
						if !strings.Contains(strings.Join(functions, "\n"), known) {
							continue
						}
						if sampleTypes[kind] == nil {
							sampleTypes[kind] = map[string]bool{}
						}
						sampleTypes[kind][profileString(t, request.Dictionary, profile.SampleType.TypeStrindex)] = true
						seen[kind] = true
					}
				}
			}
		case <-deadline.C:
			t.Fatalf("missing native workload profiles: received %v\n%s", seen, app.Logs())
		}
	}
	for kind, expected := range map[string][]string{
		"cpu":       {"samples", "cpu"},
		"heap":      {"alloc_objects", "alloc_space", "inuse_objects", "inuse_space"},
		"goroutine": {"goroutine"},
		"mutex":     {"contentions", "delay"},
		"block":     {"contentions", "delay"},
	} {
		for _, sampleType := range expected {
			if !sampleTypes[kind][sampleType] {
				t.Errorf("%s lost native sample type %q", kind, sampleType)
			}
		}
	}
	assertProfileEndpointsAbsent(t, app)
	app.Stop(t)
}

func TestDefaultProfilingEndpointsAbsent(t *testing.T) {
	app := startApp(t, fixtureConfig{})
	assertProfileEndpointsAbsent(t, app)
}

func assertProfileEndpointsAbsent(t *testing.T, app *fixtureApp) {
	t.Helper()
	for _, base := range []string{app.URL, app.ControlURL, app.MetricsURL} {
		for _, path := range []string{"/debug/pprof/", "/debug/pprof/profile", "/debug/pprof/heap", "/debug/pprof/goroutine", "/debug/pprof/mutex", "/debug/pprof/block"} {
			profileGET(t, base+path, http.StatusNotFound)
		}
	}
}

func profileGET(t *testing.T, url string, status int) []byte {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("GET %s: status %d, want %d: %s", url, response.StatusCode, status, body)
	}
	return body
}

func decodeProfileExport(t *testing.T, export exportRequest) *collector.ExportProfilesServiceRequest {
	t.Helper()
	if export.Path != "/v1/profiles" {
		t.Fatalf("profile path %q", export.Path)
	}
	if export.Header.Get("Content-Type") != "application/x-protobuf" {
		t.Fatalf("profile content type %q", export.Header.Get("Content-Type"))
	}
	request := new(collector.ExportProfilesServiceRequest)
	if err := proto.Unmarshal(export.Body, request); err != nil {
		t.Fatalf("official profile decoder: %v", err)
	}
	if request.Dictionary == nil || len(request.Dictionary.StringTable) == 0 || request.Dictionary.StringTable[0] != "" {
		t.Fatal("missing native dictionary null entry")
	}
	if len(request.ResourceProfiles) == 0 {
		t.Fatal("missing resource profiles")
	}
	return request
}

func profileString(t *testing.T, dictionary *profiles.ProfilesDictionary, index int32) string {
	t.Helper()
	if index < 0 || int(index) >= len(dictionary.StringTable) {
		t.Fatalf("invalid string index %d", index)
	}
	return dictionary.StringTable[index]
}

func profileKind(t *testing.T, dictionary *profiles.ProfilesDictionary, profile *profiles.Profile) string {
	t.Helper()
	for _, index := range profile.AttributeIndices {
		if index <= 0 || int(index) >= len(dictionary.AttributeTable) {
			t.Fatalf("invalid profile attribute index %d", index)
		}
		attribute := dictionary.AttributeTable[index]
		if profileString(t, dictionary, attribute.KeyStrindex) == "profile.type" {
			return attribute.Value.GetStringValue()
		}
	}
	t.Fatal("missing profile.type")
	return ""
}

func validateNativeProfile(t *testing.T, dictionary *profiles.ProfilesDictionary, profile *profiles.Profile, kind string) []string {
	t.Helper()
	if len(profile.ProfileId) != 16 {
		t.Fatal("invalid profile ID")
	}
	if profile.TimeUnixNano == 0 || profile.TimeUnixNano > uint64(time.Now().Add(time.Minute).UnixNano()) || profile.DurationNano == 0 {
		t.Fatalf("invalid profile interval %d/%d", profile.TimeUnixNano, profile.DurationNano)
	}
	if len(profile.OriginalPayload) != 0 || profile.OriginalPayloadFormat != "" {
		t.Fatal("opaque profile fallback retained")
	}
	if profile.SampleType == nil {
		t.Fatal("missing native sample type")
	}
	typ := profileString(t, dictionary, profile.SampleType.TypeStrindex)
	unit := profileString(t, dictionary, profile.SampleType.UnitStrindex)
	allowed := map[string]map[string]string{
		"cpu":       {"samples": "count", "cpu": "nanoseconds"},
		"heap":      {"alloc_objects": "count", "alloc_space": "bytes", "inuse_objects": "count", "inuse_space": "bytes"},
		"goroutine": {"goroutine": "count"},
		"mutex":     {"contentions": "count", "delay": "nanoseconds"},
		"block":     {"contentions": "count", "delay": "nanoseconds"},
	}
	if expected, ok := allowed[kind][typ]; !ok || expected != unit {
		t.Fatalf("%s sample type/unit %q/%q", kind, typ, unit)
	}
	if len(profile.Samples) == 0 {
		t.Fatalf("%s native samples empty", kind)
	}
	var functions []string
	positive := false
	for _, sample := range profile.Samples {
		if len(sample.Values) != 1 || sample.Values[0] < 0 {
			t.Fatalf("invalid sample values %v", sample.Values)
		}
		positive = positive || sample.Values[0] > 0
		if sample.StackIndex <= 0 || int(sample.StackIndex) >= len(dictionary.StackTable) {
			t.Fatalf("invalid stack index %d", sample.StackIndex)
		}
		stack := dictionary.StackTable[sample.StackIndex]
		if len(stack.LocationIndices) == 0 {
			t.Fatal("empty sample stack")
		}
		for _, index := range stack.LocationIndices {
			if index <= 0 || int(index) >= len(dictionary.LocationTable) {
				t.Fatalf("invalid location index %d", index)
			}
			location := dictionary.LocationTable[index]
			if location.MappingIndex < 0 || int(location.MappingIndex) >= len(dictionary.MappingTable) {
				t.Fatalf("invalid mapping index %d", location.MappingIndex)
			}
			mapping := dictionary.MappingTable[location.MappingIndex]
			if name := profileString(t, dictionary, mapping.FilenameStrindex); strings.ContainsAny(name, "/\\") {
				t.Fatalf("mapping source path leaked: %q", name)
			}
			for _, line := range location.Lines {
				if line.FunctionIndex <= 0 || int(line.FunctionIndex) >= len(dictionary.FunctionTable) {
					t.Fatalf("invalid function index %d", line.FunctionIndex)
				}
				function := dictionary.FunctionTable[line.FunctionIndex]
				profileString(t, dictionary, function.SystemNameStrindex)
				functions = append(functions, profileString(t, dictionary, function.NameStrindex))
				if name := profileString(t, dictionary, function.FilenameStrindex); strings.ContainsAny(name, "/\\") {
					t.Fatalf("source path leaked: %q", name)
				}
			}
		}
		keys := map[string]bool{}
		for _, index := range sample.AttributeIndices {
			if index <= 0 || int(index) >= len(dictionary.AttributeTable) {
				t.Fatalf("invalid sample attribute index %d", index)
			}
			attribute := dictionary.AttributeTable[index]
			key := profileString(t, dictionary, attribute.KeyStrindex)
			if keys[key] {
				t.Fatalf("duplicate sample key %q", key)
			}
			keys[key] = true
			profileString(t, dictionary, attribute.UnitStrindex)
		}
	}
	if !positive {
		t.Fatalf("%s/%s samples all zero", kind, typ)
	}
	return functions
}
