package otlp_test

import (
	"bytes"
	"testing"
	"time"

	pprof "github.com/google/pprof/profile"
	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/privacy"
	"github.com/kzs0/bedrock/trace/otlp"
	collector "go.opentelemetry.io/proto/otlp/collector/profiles/v1development"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"
)

func TestProfileSampleAttributesHaveUniqueKeys(t *testing.T) {
	start := time.Unix(1700000000, 0)
	parsed, err := pprof.ParseData(fixturePprof(t, start, start.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	parsed.Sample[0].Label = map[string][]string{"tag": {"first", "second"}}
	var raw bytes.Buffer
	if err := parsed.Write(&raw); err != nil {
		t.Fatal(err)
	}
	encoded, err := otlp.EncodeProfileChecked("svc", attr.EmptySet, "cpu", raw.Bytes(), start, start.Add(time.Second), otlp.ProfileEncodingOptions{LabelFilter: func(a attr.Attr) (attr.Attr, bool) { return a, true }})
	if err != nil {
		t.Fatal(err)
	}
	var request collector.ExportProfilesServiceRequest
	if err := proto.Unmarshal(encoded, &request); err != nil {
		t.Fatal(err)
	}
	dict := request.Dictionary
	for _, resource := range request.ResourceProfiles {
		for _, scope := range resource.ScopeProfiles {
			for _, profile := range scope.Profiles {
				for _, sample := range profile.Samples {
					seen := map[string]bool{}
					for _, index := range sample.AttributeIndices {
						key := dict.StringTable[dict.AttributeTable[index].KeyStrindex]
						if seen[key] {
							t.Fatalf("duplicate sample attribute key %q", key)
						}
						seen[key] = true
					}
				}
			}
		}
	}
}

func TestRepeatedProfileLabelsPreserveValuesAndUnits(t *testing.T) {
	start := time.Unix(1700000000, 0)
	parsed, err := pprof.ParseData(fixturePprof(t, start, start.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	parsed.Sample[0].Label = map[string][]string{"tag": {"first", "second"}, "mixed": {"text"}}
	parsed.Sample[0].NumLabel = map[string][]int64{"weight": {0, 7}, "mixed": {1, 2}}
	parsed.Sample[0].NumUnit = map[string][]string{"weight": {"bytes", "bytes"}, "mixed": {"bytes", "nanoseconds"}}
	var raw bytes.Buffer
	if err := parsed.Write(&raw); err != nil {
		t.Fatal(err)
	}
	encoded, err := otlp.EncodeProfileChecked("svc", attr.EmptySet, "cpu", raw.Bytes(), start, start.Add(time.Second), otlp.ProfileEncodingOptions{LabelFilter: func(a attr.Attr) (attr.Attr, bool) { return a, true }})
	if err != nil {
		t.Fatal(err)
	}
	var request collector.ExportProfilesServiceRequest
	if err := proto.Unmarshal(encoded, &request); err != nil {
		t.Fatal(err)
	}
	dictionary := request.Dictionary
	sample := request.ResourceProfiles[0].ScopeProfiles[0].Profiles[0].Samples[0]
	found := map[string]bool{}
	for _, index := range sample.AttributeIndices {
		entry := dictionary.AttributeTable[index]
		key := dictionary.StringTable[entry.KeyStrindex]
		if found[key] {
			t.Fatalf("duplicate %q", key)
		}
		found[key] = true
		values := entry.Value.GetArrayValue().GetValues()
		switch key {
		case "tag":
			if len(values) != 2 || values[0].GetStringValue() != "first" || values[1].GetStringValue() != "second" {
				t.Fatal("string label values lost")
			}
		case "weight":
			if dictionary.StringTable[entry.UnitStrindex] != "bytes" || len(values) != 2 || values[0].GetIntValue() != 0 || values[1].GetIntValue() != 7 {
				t.Fatal("numeric values or units lost")
			}
			if _, ok := values[0].Value.(*common.AnyValue_IntValue); !ok {
				t.Fatal("zero numeric label lost its type")
			}
		case "mixed":
			if entry.UnitStrindex != 0 || len(values) != 3 || values[0].GetStringValue() != "text" {
				t.Fatal("heterogeneous labels lost")
			}
			for i, unit := range []string{"bytes", "nanoseconds"} {
				fields := map[string]*common.AnyValue{}
				for _, kv := range values[i+1].GetKvlistValue().GetValues() {
					fields[kv.Key] = kv.Value
				}
				if fields["unit"].GetStringValue() != unit || fields["value"].GetIntValue() != int64(i+1) {
					t.Fatal("per-value unit lost")
				}
			}
		}
	}
	if len(found) != 3 {
		t.Fatal("profile labels missing")
	}
}

func TestProfileLabelFilterRenamesMergeKeys(t *testing.T) {
	start := time.Unix(1700000000, 0)
	parsed, err := pprof.ParseData(fixturePprof(t, start, start.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	parsed.Sample[0].Label = map[string][]string{"first": {"a", "b"}, "second": {"c"}}
	var raw bytes.Buffer
	if err := parsed.Write(&raw); err != nil {
		t.Fatal(err)
	}
	encoded, err := otlp.EncodeProfileChecked("svc", attr.EmptySet, "cpu", raw.Bytes(), start, start.Add(time.Second), otlp.ProfileEncodingOptions{LabelFilter: func(a attr.Attr) (attr.Attr, bool) { return a.WithKey("merged"), true }})
	if err != nil {
		t.Fatal(err)
	}
	var request collector.ExportProfilesServiceRequest
	if err := proto.Unmarshal(encoded, &request); err != nil {
		t.Fatal(err)
	}
	indices := request.ResourceProfiles[0].ScopeProfiles[0].Profiles[0].Samples[0].AttributeIndices
	if len(indices) != 1 {
		t.Fatal("renamed label collision not merged")
	}
	values := request.Dictionary.AttributeTable[indices[0]].Value.GetArrayValue().GetValues()
	if len(values) != 3 || values[0].GetStringValue() != "a" || values[1].GetStringValue() != "b" || values[2].GetStringValue() != "c" {
		t.Fatal("renamed values lost")
	}
}

func TestLongProfileLabelArraysAreNotSilentlyTruncated(t *testing.T) {
	start := time.Unix(1700000000, 0)
	parsed, err := pprof.ParseData(fixturePprof(t, start, start.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	labels := make([]string, 100)
	for i := range labels {
		labels[i] = "value"
	}
	parsed.Sample[0].Label = map[string][]string{"tag": labels}
	var raw bytes.Buffer
	if err := parsed.Write(&raw); err != nil {
		t.Fatal(err)
	}
	encoded, err := otlp.EncodeProfileChecked("svc", attr.EmptySet, "cpu", raw.Bytes(), start, start.Add(time.Second), otlp.ProfileEncodingOptions{LabelFilter: func(a attr.Attr) (attr.Attr, bool) { return a, true }})
	if err != nil {
		return
	}
	var request collector.ExportProfilesServiceRequest
	if err := proto.Unmarshal(encoded, &request); err != nil {
		t.Fatal(err)
	}
	sample := request.ResourceProfiles[0].ScopeProfiles[0].Profiles[0].Samples[0]
	if len(sample.AttributeIndices) != 1 {
		t.Fatalf("label indices=%v", sample.AttributeIndices)
	}
	values := request.Dictionary.AttributeTable[sample.AttributeIndices[0]].Value.GetArrayValue().GetValues()
	if len(values) != len(labels) {
		t.Fatalf("label array silently shortened from %d to %d", len(labels), len(values))
	}
}

func TestProfilePolicyTruncationMarkersSurviveEncoding(t *testing.T) {
	start := time.Unix(1700000000, 0)
	parsed, err := pprof.ParseData(fixturePprof(t, start, start.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	labels := make([]string, 100)
	for i := range labels {
		labels[i] = "value"
	}
	parsed.Sample[0].Label = map[string][]string{"tag": labels}
	var raw bytes.Buffer
	if err := parsed.Write(&raw); err != nil {
		t.Fatal(err)
	}
	policy := (&privacy.Policy{AllowProfileLabels: true}).Snapshot()
	encoded, err := otlp.EncodeProfileChecked("svc", attr.EmptySet, "cpu", raw.Bytes(), start, start.Add(time.Second), otlp.ProfileEncodingOptions{LabelFilter: policy.Attribute})
	if err != nil {
		t.Fatal(err)
	}
	var request collector.ExportProfilesServiceRequest
	if err := proto.Unmarshal(encoded, &request); err != nil {
		t.Fatal(err)
	}
	sample := request.ResourceProfiles[0].ScopeProfiles[0].Profiles[0].Samples[0]
	values := request.Dictionary.AttributeTable[sample.AttributeIndices[0]].Value.GetArrayValue().GetValues()
	if len(values) != 64 || values[len(values)-1].GetStringValue() != "[truncated]" {
		t.Fatal("configured policy truncation marker was lost")
	}
}

func TestCheckedProfileRejectsOversizedAttributeGraphs(t *testing.T) {
	start := time.Unix(1700000000, 0)
	parsed, err := pprof.ParseData(fixturePprof(t, start, start.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	parsed.Sample[0].Label = map[string][]string{"tag": {"value"}}
	var raw bytes.Buffer
	if err := parsed.Write(&raw); err != nil {
		t.Fatal(err)
	}
	deep := any("leaf")
	for i := 0; i < 10; i++ {
		deep = []any{deep}
	}
	var tree func(int) any
	tree = func(depth int) any {
		if depth == 0 {
			return "leaf"
		}
		child := tree(depth - 1)
		return []any{child, child, child, child}
	}
	for name, value := range map[string]any{"depth": deep, "nodes": tree(7)} {
		t.Run(name, func(t *testing.T) {
			_, err := otlp.EncodeProfileChecked("svc", attr.EmptySet, "cpu", raw.Bytes(), start, start.Add(time.Second), otlp.ProfileEncodingOptions{LabelFilter: func(a attr.Attr) (attr.Attr, bool) { return attr.Any(a.Key, value), true }})
			if err == nil {
				t.Fatal("checked API silently shortened attribute graph")
			}
		})
	}
}
