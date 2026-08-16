package prometheus

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/metric"
)

func FuzzEscapeHelp(f *testing.F) {
	for _, seed := range []string{
		"",
		"plain help",
		"path\\to\\file",
		"first line\nsecond line",
		"\\\n",
		"invalid utf8: \xff",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 1024 {
			return
		}
		got := escapeHelp(input)
		want := referenceEscapeHelp(input)
		if got != want {
			t.Fatalf("escapeHelp(%q) = %q, want %q", input, got, want)
		}
		if strings.ContainsRune(got, '\n') {
			t.Fatalf("escaped HELP contains a literal newline: %q", got)
		}
		if len(got) > 2*len(input) {
			t.Fatalf("escaped HELP grew from %d to %d bytes", len(input), len(got))
		}
	})
}

func FuzzMetricLabelValueEscaping(f *testing.F) {
	for _, seed := range []string{
		"",
		"plain",
		"quote: \"",
		"backslash: \\",
		"line1\nline2",
		"tabs\tand\rreturns",
		"invalid utf8: \xff",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 1024 {
			return
		}
		var output bytes.Buffer
		writeMetricLine(&output, "fuzz_metric", [][2]string{{"label", value}}, 1)

		const prefix = "fuzz_metric{label="
		const suffix = "} 1\n"
		line := output.String()
		if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, suffix) {
			t.Fatalf("malformed metric line %q", line)
		}
		if strings.Count(line, "\n") != 1 {
			t.Fatalf("label value escaped the metric line: %q", line)
		}

		quoted := line[len(prefix) : len(line)-len(suffix)]
		want := `"` + referenceEscapeLabelValue(value) + `"`
		if quoted != want {
			t.Fatalf("label encoding = %q, want %q", quoted, want)
		}
	})
}

func FuzzEncodeProtocolBoundaries(f *testing.F) {
	f.Add(uint8(0), "metric_name", "label_name", "help", "value")
	f.Add(uint8(0), "service:metric_total", "label", "help", "value")
	f.Add(uint8(0), "metric\ninjected", "label", "help", "value")
	f.Add(uint8(0), "metric", "label:invalid", "help", "value")
	f.Add(uint8(0), "metric", "__reserved", "help", "value")
	f.Add(uint8(1), "histogram", "le", "help", "value")
	f.Add(uint8(0), "metric", "label\ninjected", "help", "value")
	f.Add(uint8(0), "metric", "label", "help\nnext", "value\nnext")
	f.Add(uint8(0), "metric", "label", "bad\xff", "bad\xff")

	f.Fuzz(func(t *testing.T, kind uint8, metricName, labelName, help, value string) {
		if len(metricName) > 128 || len(labelName) > 128 || len(help) > 1024 || len(value) > 1024 {
			return
		}
		metricType := metric.TypeGauge
		if kind%2 == 1 {
			metricType = metric.TypeHistogram
		}
		family := metric.MetricFamily{
			Name: metricName, Help: help, Type: metricType,
			Metrics: []metric.Metric{{Labels: attr.NewSet(attr.String(labelName, value)), Value: 1}},
		}
		var output bytes.Buffer
		err := Encode(&output, []metric.MetricFamily{family})
		valid := referenceLegacyMetricName(metricName) && referenceLegacyLabelName(labelName) &&
			!strings.HasPrefix(labelName, "__") && (metricType != metric.TypeHistogram || labelName != "le") &&
			utf8.ValidString(help) && utf8.ValidString(value)
		if valid && err != nil {
			t.Fatalf("valid exposition rejected: %v", err)
		}
		if !valid && err == nil {
			t.Fatalf("invalid exposition accepted: metric=%q label=%q help=%q value=%q", metricName, labelName, help, value)
		}
		if err != nil && output.Len() != 0 {
			t.Fatalf("rejected exposition produced partial output: %q", output.String())
		}
		if err == nil {
			wantLines := 2
			if metricType == metric.TypeHistogram {
				wantLines = 4 // TYPE, +Inf bucket, sum, and count.
			}
			if help != "" {
				wantLines++
			}
			if strings.Count(output.String(), "\n") != wantLines {
				t.Fatalf("input injected an exposition line: %q", output.String())
			}
		}
	})
}

func FuzzEncodeHistogramStructure(f *testing.F) {
	for _, seed := range []struct {
		buckets []metric.Bucket
		total   uint64
	}{
		{nil, 0},
		{[]metric.Bucket{{UpperBound: 1, Count: 0}, {UpperBound: 2, Count: 2}}, 3},
		{[]metric.Bucket{{UpperBound: 1, Count: 2}, {UpperBound: 1, Count: 2}}, 2},
		{[]metric.Bucket{{UpperBound: math.NaN(), Count: 0}}, 0},
		{[]metric.Bucket{{UpperBound: math.Inf(1), Count: 1}}, 1},
		{[]metric.Bucket{{UpperBound: 1, Count: 2}, {UpperBound: 2, Count: 1}}, 2},
		{[]metric.Bucket{{UpperBound: 1, Count: 2}}, 1},
	} {
		f.Add(encodeHistogramBuckets(seed.buckets), seed.total)
	}

	f.Fuzz(func(t *testing.T, encoded []byte, total uint64) {
		if len(encoded) > 16*32 {
			return
		}
		buckets := decodeHistogramBuckets(encoded)
		family := metric.MetricFamily{
			Name: "fuzz_histogram", Type: metric.TypeHistogram,
			Metrics: []metric.Metric{{Buckets: buckets, Count: total}},
		}
		var output bytes.Buffer
		err := Encode(&output, []metric.MetricFamily{family})
		valid := referenceValidHistogram(buckets, total)
		if valid && err != nil {
			t.Fatalf("valid histogram rejected: buckets=%#v total=%d error=%v", buckets, total, err)
		}
		if !valid && err == nil {
			t.Fatalf("invalid histogram accepted: buckets=%#v total=%d", buckets, total)
		}
		if err != nil && output.Len() != 0 {
			t.Fatalf("invalid histogram produced partial output: %q", output.String())
		}
	})
}

func encodeHistogramBuckets(buckets []metric.Bucket) []byte {
	encoded := make([]byte, 16*len(buckets))
	for i, bucket := range buckets {
		binary.LittleEndian.PutUint64(encoded[i*16:], math.Float64bits(bucket.UpperBound))
		binary.LittleEndian.PutUint64(encoded[i*16+8:], bucket.Count)
	}
	return encoded
}

func decodeHistogramBuckets(encoded []byte) []metric.Bucket {
	buckets := make([]metric.Bucket, 0, len(encoded)/16)
	for len(encoded) >= 16 {
		buckets = append(buckets, metric.Bucket{
			UpperBound: math.Float64frombits(binary.LittleEndian.Uint64(encoded)),
			Count:      binary.LittleEndian.Uint64(encoded[8:]),
		})
		encoded = encoded[16:]
	}
	return buckets
}

func referenceValidHistogram(buckets []metric.Bucket, total uint64) bool {
	var previousBound float64
	var previousCount uint64
	for i, bucket := range buckets {
		if math.IsNaN(bucket.UpperBound) || math.IsInf(bucket.UpperBound, 0) || bucket.Count > total {
			return false
		}
		if i > 0 && (bucket.UpperBound <= previousBound || bucket.Count < previousCount) {
			return false
		}
		previousBound = bucket.UpperBound
		previousCount = bucket.Count
	}
	return true
}

func referenceLegacyMetricName(name string) bool {
	if name == "" || !referenceASCIILetterUnderscoreOrColon(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !referenceASCIILetterUnderscoreOrColon(name[i]) && (name[i] < '0' || name[i] > '9') {
			return false
		}
	}
	return true
}

func referenceLegacyLabelName(name string) bool {
	if name == "" || !referenceASCIILetterOrUnderscore(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !referenceASCIILetterOrUnderscore(name[i]) && (name[i] < '0' || name[i] > '9') {
			return false
		}
	}
	return true
}

func referenceASCIILetterUnderscoreOrColon(ch byte) bool {
	return referenceASCIILetterOrUnderscore(ch) || ch == ':'
}

func referenceASCIILetterOrUnderscore(ch byte) bool {
	return ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}

func referenceEscapeHelp(input string) string {
	var result strings.Builder
	result.Grow(len(input))
	for i := 0; i < len(input); i++ {
		switch input[i] {
		case '\\':
			result.WriteString(`\\`)
		case '\n':
			result.WriteString(`\n`)
		default:
			result.WriteByte(input[i])
		}
	}
	return result.String()
}

func referenceEscapeLabelValue(input string) string {
	var result strings.Builder
	result.Grow(len(input))
	for i := 0; i < len(input); i++ {
		switch input[i] {
		case '\\':
			result.WriteString(`\\`)
		case '"':
			result.WriteString(`\"`)
		case '\n':
			result.WriteString(`\n`)
		default:
			result.WriteByte(input[i])
		}
	}
	return result.String()
}
