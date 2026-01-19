package prometheus

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/internal"
	"github.com/kzs0/bedrock/metric"
)

// Encode writes metrics in Prometheus text exposition format.
func Encode(w io.Writer, families []metric.MetricFamily) error {
	// Sort families by name for consistent output
	sort.Slice(families, func(i, j int) bool {
		return families[i].Name < families[j].Name
	})

	buf := internal.GetBuffer()
	defer internal.PutBuffer(buf)

	for _, fam := range families {
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

// writeMetricLine writes a metric with labels.
func writeMetricLine(w io.Writer, name string, labelPairs [][2]string, value float64) {
	if len(labelPairs) == 0 {
		fmt.Fprintf(w, "%s %s\n", name, formatFloat(value))
		return
	}

	fmt.Fprintf(w, "%s{", name)
	for i, pair := range labelPairs {
		if i > 0 {
			fmt.Fprint(w, ",")
		}
		fmt.Fprintf(w, "%s=%q", pair[0], pair[1])
	}
	fmt.Fprintf(w, "} %s\n", formatFloat(value))
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
