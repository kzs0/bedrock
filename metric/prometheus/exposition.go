package prometheus

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/internal"
	"github.com/kzs0/bedrock/metric"
)

// Encode writes metrics in Prometheus text exposition format.
func Encode(w io.Writer, families []metric.MetricFamily) error {
	if err := validateFamilies(families); err != nil {
		return err
	}

	// Sort a shallow copy so deterministic output does not reorder the caller's
	// family slice.
	sortedFamilies := append([]metric.MetricFamily(nil), families...)
	sort.Slice(sortedFamilies, func(i, j int) bool {
		return sortedFamilies[i].Name < sortedFamilies[j].Name
	})

	buf := internal.GetBuffer()
	defer internal.PutBuffer(buf)

	for _, fam := range sortedFamilies {
		if len(fam.Metrics) == 0 {
			continue
		}
		// Write HELP line
		if fam.Help != "" {
			fmt.Fprintf(buf, "# HELP %s %s\n", fam.Name, escapeHelp(fam.Help))
		}

		// Write TYPE line
		fmt.Fprintf(buf, "# TYPE %s %s\n", fam.Name, fam.Type)

		// Write metric values
		for _, m := range fam.Metrics {
			labelPairs := attrsToLabels(m.Labels)

			switch fam.Type {
			case metric.TypeCounter, metric.TypeGauge:
				writeMetricLine(buf, fam.Name, labelPairs, m.Value)
			case metric.TypeHistogram:
				writeHistogram(buf, fam.Name, m, labelPairs)
			}
		}
	}

	_, err := w.Write(buf.Bytes())
	return err
}

func validateFamilies(families []metric.MetricFamily) error {
	owners := make(map[string]string)
	for _, fam := range families {
		if len(fam.Metrics) == 0 {
			continue
		}
		if err := validateFamily(fam); err != nil {
			return err
		}

		names := []string{fam.Name}
		if fam.Type == metric.TypeHistogram {
			names = append(names, fam.Name+"_bucket", fam.Name+"_sum", fam.Name+"_count")
		}
		for _, name := range names {
			if owner, exists := owners[name]; exists {
				return fmt.Errorf("prometheus: metric name %q from family %q collides with family %q", name, fam.Name, owner)
			}
			owners[name] = fam.Name
		}
	}
	return nil
}

// writeMetricLine writes a metric with labels.
func writeMetricLine(w io.Writer, name string, labelPairs [][2]string, value float64) {
	if len(labelPairs) == 0 {
		_, _ = fmt.Fprintf(w, "%s %s\n", name, formatFloat(value))
		return
	}

	_, _ = fmt.Fprintf(w, "%s{", name)
	for i, pair := range labelPairs {
		if i > 0 {
			_, _ = fmt.Fprint(w, ",")
		}
		_, _ = fmt.Fprintf(w, `%s="%s"`, pair[0], escapeLabelValue(pair[1]))
	}
	_, _ = fmt.Fprintf(w, "} %s\n", formatFloat(value))
}

// writeHistogram writes histogram buckets, sum, and count.
func writeHistogram(w io.Writer, name string, m metric.Metric, labelPairs [][2]string) {
	// Write buckets
	for _, b := range m.Buckets {
		bucketLabels := make([][2]string, len(labelPairs), len(labelPairs)+1)
		copy(bucketLabels, labelPairs)
		bucketLabels = append(bucketLabels, [2]string{"le", formatFloat(b.UpperBound)})
		writeMetricLine(w, name+"_bucket", bucketLabels, float64(b.Count))
	}

	// Write +Inf bucket
	infLabels := make([][2]string, len(labelPairs), len(labelPairs)+1)
	copy(infLabels, labelPairs)
	infLabels = append(infLabels, [2]string{"le", "+Inf"})
	writeMetricLine(w, name+"_bucket", infLabels, float64(m.Count))

	// Write sum and count
	writeMetricLine(w, name+"_sum", labelPairs, m.Sum)
	writeMetricLine(w, name+"_count", labelPairs, float64(m.Count))
}

// attrsToLabels converts an attr.Set to label pairs.
func attrsToLabels(labels attr.Set) [][2]string {
	attrs := labels.Attrs()
	pairs := make([][2]string, len(attrs))
	for i, a := range attrs {
		pairs[i] = [2]string{a.Key, a.Value.String()}
	}
	return pairs
}

func validateFamily(fam metric.MetricFamily) error {
	if !validMetricName(fam.Name) {
		return fmt.Errorf("prometheus: invalid metric name %q", fam.Name)
	}
	if !utf8.ValidString(fam.Help) {
		return fmt.Errorf("prometheus: metric %q help contains invalid UTF-8", fam.Name)
	}
	switch fam.Type {
	case metric.TypeCounter, metric.TypeGauge, metric.TypeHistogram:
	default:
		return fmt.Errorf("prometheus: metric %q has invalid type %q", fam.Name, fam.Type)
	}

	for i, m := range fam.Metrics {
		for _, label := range m.Labels.Attrs() {
			if !validLabelName(label.Key) || strings.HasPrefix(label.Key, "__") {
				return fmt.Errorf("prometheus: invalid label name %q for metric %q", label.Key, fam.Name)
			}
			if fam.Type == metric.TypeHistogram && label.Key == "le" {
				return fmt.Errorf("prometheus: histogram metric %q uses reserved label %q", fam.Name, label.Key)
			}
			if !utf8.ValidString(label.Value.String()) {
				return fmt.Errorf("prometheus: label %q for metric %q contains invalid UTF-8", label.Key, fam.Name)
			}
		}
		if fam.Type == metric.TypeHistogram {
			if err := validateHistogramMetric(fam.Name, i, m); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateHistogramMetric(name string, metricIndex int, m metric.Metric) error {
	var previousBound float64
	var previousCount uint64
	for i, bucket := range m.Buckets {
		if math.IsNaN(bucket.UpperBound) || math.IsInf(bucket.UpperBound, 0) {
			return fmt.Errorf("prometheus: histogram metric %q sample %d bucket %d has non-finite bound %g", name, metricIndex, i, bucket.UpperBound)
		}
		if i > 0 && bucket.UpperBound <= previousBound {
			return fmt.Errorf("prometheus: histogram metric %q sample %d bucket bounds are not strictly increasing at bucket %d", name, metricIndex, i)
		}
		if i > 0 && bucket.Count < previousCount {
			return fmt.Errorf("prometheus: histogram metric %q sample %d bucket counts decrease at bucket %d", name, metricIndex, i)
		}
		if bucket.Count > m.Count {
			return fmt.Errorf("prometheus: histogram metric %q sample %d bucket %d count %d exceeds total count %d", name, metricIndex, i, bucket.Count, m.Count)
		}
		previousBound = bucket.UpperBound
		previousCount = bucket.Count
	}
	return nil
}

func validMetricName(name string) bool {
	if name == "" || !validMetricInitial(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !validMetricInitial(name[i]) && (name[i] < '0' || name[i] > '9') {
			return false
		}
	}
	return true
}

func validMetricInitial(ch byte) bool {
	return ch == '_' || ch == ':' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}

func validLabelName(name string) bool {
	if name == "" || !validLabelInitial(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !validLabelInitial(name[i]) && (name[i] < '0' || name[i] > '9') {
			return false
		}
	}
	return true
}

func validLabelInitial(ch byte) bool {
	return ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}

// escapeLabelValue applies the Prometheus text-format escaping rules. Only
// backslash, double quote, and line feed have escape sequences in this format.
func escapeLabelValue(s string) string {
	var escaped strings.Builder
	escaped.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			escaped.WriteString(`\\`)
		case '"':
			escaped.WriteString(`\"`)
		case '\n':
			escaped.WriteString(`\n`)
		default:
			escaped.WriteByte(s[i])
		}
	}
	return escaped.String()
}

// formatFloat formats a float64 for Prometheus output.
func formatFloat(v float64) string {
	if math.IsNaN(v) {
		return "NaN"
	}
	if math.IsInf(v, 1) {
		return "+Inf"
	}
	if math.IsInf(v, -1) {
		return "-Inf"
	}
	return fmt.Sprintf("%g", v)
}

// escapeHelp escapes a help string for Prometheus format.
func escapeHelp(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}
