package otlp_test

import (
	"bytes"
	"testing"
	"time"

	pprof "github.com/google/pprof/profile"
	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/trace/otlp"
	collector "go.opentelemetry.io/proto/otlp/collector/profiles/v1development"
	"google.golang.org/protobuf/proto"
)

func TestProfileSampleValuesPreserveZero(t *testing.T) {
	start := time.Unix(1700000000, 0)
	p, err := pprof.ParseData(fixturePprof(t, start, start.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []int64{0, -7, 7} {
		for _, sample := range p.Sample {
			for i := range sample.Value {
				sample.Value[i] = want
			}
		}
		var raw bytes.Buffer
		if err := p.Write(&raw); err != nil {
			t.Fatal(err)
		}
		encoded, err := otlp.EncodeProfileChecked("svc", attr.EmptySet, "heap", raw.Bytes(), start, start.Add(time.Second), otlp.ProfileEncodingOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var request collector.ExportProfilesServiceRequest
		if err := proto.Unmarshal(encoded, &request); err != nil {
			t.Fatal(err)
		}
		for _, resource := range request.ResourceProfiles {
			for _, scope := range resource.ScopeProfiles {
				for _, profile := range scope.Profiles {
					for _, sample := range profile.Samples {
						if len(sample.Values) != 1 || sample.Values[0] != want {
							t.Fatalf("sample values %v, want [%d]", sample.Values, want)
						}
					}
				}
			}
		}
	}
}
