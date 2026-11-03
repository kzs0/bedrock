package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type codecLabel struct{ Name, Value []byte }
type codecBucket struct {
	Bound string
	Count uint64
}
type codecSample struct {
	Labels  []codecLabel
	Value   string
	Buckets []codecBucket
	Count   uint64
	Sum     string
}
type codecFamily struct {
	Name, Help []byte
	Type       string
	Samples    []codecSample
}
type codecInput struct {
	Families    []codecFamily
	RejectWrite bool
}
type codecOutput struct {
	Text, Error string
	Unchanged   bool
	WriteCalls  int `json:"write_calls"`
}

func metricCodecCall(t *testing.T, app *fixtureApp, input codecInput, status int) codecOutput {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Post(app.URL+"/encode", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var result codecOutput
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status || !result.Unchanged {
		t.Fatalf("codec status=%d result=%+v", response.StatusCode, result)
	}
	return result
}

func TestMetricCodecRejectsInvalidFamiliesBeforeWriting(t *testing.T) {
	app := startApp(t, fixtureConfig{Scenario: "metric-codec"})
	gauge := func(name string) codecFamily {
		return codecFamily{Name: []byte(name), Type: "gauge", Samples: []codecSample{{Value: "1"}}}
	}
	hist := func(buckets ...codecBucket) codecFamily {
		return codecFamily{Name: []byte("duration"), Type: "histogram", Samples: []codecSample{{Buckets: buckets, Count: 3, Sum: "2"}}}
	}
	cases := []struct {
		name, want string
		family     codecFamily
		extra      []codecFamily
	}{
		{"empty name", "invalid metric name", gauge(""), nil},
		{"initial digit", "invalid metric name", gauge("1bad"), nil},
		{"name punctuation", "invalid metric name", gauge("bad-name"), nil},
		{"invalid help", "help contains invalid UTF-8", func() codecFamily { f := gauge("valid"); f.Help = []byte{0xff}; return f }(), nil},
		{"unknown type", "invalid type", func() codecFamily { f := gauge("valid"); f.Type = "summary"; return f }(), nil},
		{"empty label", "invalid label name", func() codecFamily {
			f := gauge("valid")
			f.Samples[0].Labels = []codecLabel{{Name: nil, Value: []byte("x")}}
			return f
		}(), nil},
		{"digit label", "invalid label name", func() codecFamily {
			f := gauge("valid")
			f.Samples[0].Labels = []codecLabel{{Name: []byte("1bad"), Value: []byte("x")}}
			return f
		}(), nil},
		{"punctuation label", "invalid label name", func() codecFamily {
			f := gauge("valid")
			f.Samples[0].Labels = []codecLabel{{Name: []byte("bad-name"), Value: []byte("x")}}
			return f
		}(), nil},
		{"reserved label", "invalid label name", func() codecFamily {
			f := gauge("valid")
			f.Samples[0].Labels = []codecLabel{{Name: []byte("__private"), Value: []byte("x")}}
			return f
		}(), nil},
		{"label UTF8", "contains invalid UTF-8", func() codecFamily {
			f := gauge("valid")
			f.Samples[0].Labels = []codecLabel{{Name: []byte("label"), Value: []byte{0xff}}}
			return f
		}(), nil},
		{"reserved bucket label", "reserved label", func() codecFamily {
			f := hist()
			f.Samples[0].Labels = []codecLabel{{Name: []byte("le"), Value: []byte("1")}}
			return f
		}(), nil},
		{"nan bound", "non-finite bound", hist(codecBucket{"NaN", 1}), nil},
		{"positive infinite bound", "non-finite bound", hist(codecBucket{"+Inf", 1}), nil},
		{"negative infinite bound", "non-finite bound", hist(codecBucket{"-Inf", 1}), nil},
		{"duplicate bound", "not strictly increasing", hist(codecBucket{"1", 1}, codecBucket{"1", 2}), nil},
		{"descending bounds", "not strictly increasing", hist(codecBucket{"2", 1}, codecBucket{"1", 2}), nil},
		{"decreasing counts", "counts decrease", hist(codecBucket{"1", 2}, codecBucket{"2", 1}), nil},
		{"count exceeds total", "exceeds total count", hist(codecBucket{"1", 4}), nil},
		{"duplicate family", "collides", gauge("duplicate"), []codecFamily{gauge("duplicate")}},
		{"histogram sample collision", "collides", hist(), []codecFamily{gauge("duration_count")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			families := append([]codecFamily{gauge("safe_prefix"), tc.family}, tc.extra...)
			result := metricCodecCall(t, app, codecInput{Families: families}, 400)
			if !strings.Contains(result.Error, tc.want) || result.Text != "" || result.WriteCalls != 0 {
				t.Fatalf("rejection did not precede writing: %+v", result)
			}
		})
	}
	app.Stop(t)
}

func TestMetricCodecExpositionAndWriterFailures(t *testing.T) {
	app := startApp(t, fixtureConfig{Scenario: "metric-codec"})
	input := codecInput{Families: []codecFamily{
		{Name: []byte("z_gauge"), Help: []byte("quoted \\\"\nhelp"), Type: "gauge", Samples: []codecSample{
			{Value: "NaN", Labels: []codecLabel{{Name: []byte("state"), Value: []byte("nan")}}},
			{Value: "+Inf", Labels: []codecLabel{{Name: []byte("state"), Value: []byte("positive")}}},
			{Value: "-Inf", Labels: []codecLabel{{Name: []byte("state"), Value: []byte("negative")}}},
		}},
		{Name: []byte("a_hist"), Type: "histogram", Samples: []codecSample{{Labels: []codecLabel{{Name: []byte("route"), Value: []byte("/a\"b\\c\nd")}}, Buckets: []codecBucket{{"0.5", 1}, {"2", 3}}, Count: 4, Sum: "2.5"}}},
		{Name: []byte("empty"), Type: "invalid"},
		{Name: []byte(":counter9"), Type: "counter", Samples: []codecSample{{Value: "7"}}},
	}}
	result := metricCodecCall(t, app, input, 200)
	want := `# TYPE :counter9 counter
:counter9 7
# TYPE a_hist histogram
a_hist_bucket{route="/a\"b\\c\nd",le="0.5"} 1
a_hist_bucket{route="/a\"b\\c\nd",le="2"} 3
a_hist_bucket{route="/a\"b\\c\nd",le="+Inf"} 4
a_hist_sum{route="/a\"b\\c\nd"} 2.5
a_hist_count{route="/a\"b\\c\nd"} 4
# HELP z_gauge quoted \\"\nhelp
# TYPE z_gauge gauge
z_gauge{state="nan"} NaN
z_gauge{state="positive"} +Inf
z_gauge{state="negative"} -Inf
`
	if result.Error != "" || result.Text != want {
		t.Fatalf("exposition\ngot %q\nwant %q", result.Text, want)
	}
	input.RejectWrite = true
	failed := metricCodecCall(t, app, input, 503)
	if failed.Error != "application destination unavailable" || failed.Text != "" || failed.WriteCalls != 1 {
		t.Fatalf("writer failure: %+v", failed)
	}
	empty := metricCodecCall(t, app, codecInput{}, 200)
	if empty.Error != "" || empty.Text != "" {
		t.Fatalf("empty families: %+v", empty)
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Post(app.URL+"/encode", "application/json", strings.NewReader(`{"unknown":true}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 400 || !strings.Contains(string(body), "unknown field") {
		t.Fatalf("unknown field: %d %q", response.StatusCode, body)
	}
	app.Stop(t)
}
