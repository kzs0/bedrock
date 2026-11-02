package metric

import (
	"runtime"
	"testing"
)

func TestRuntimeThreadMetricDoesNotReportGCCycles(t *testing.T) {
	r := NewRegistry("")
	c := NewRuntimeCollector(r)
	before, _ := runtime.ThreadCreateProfile(nil)
	c.Collect()
	threads, _ := runtime.ThreadCreateProfile(nil)
	for _, f := range r.Gather() {
		if f.Name == "go_threads" {
			for _, m := range f.Metrics {
				if m.Value < float64(before) || m.Value > float64(threads) {
					t.Errorf("go_threads=%v, thread profile count=%d", m.Value, threads)
				}
			}
		}
	}
}
