package metric

import (
	"math"
	"sync"
	"testing"

	"github.com/kzs0/bedrock/attr"
)

func TestCounterFractionalAccumulation(t *testing.T) {
	r := NewRegistry("")
	c := r.Counter("fractional_total", "Fractional counter")

	c.Add(0.25)
	c.Add(1.5)
	c.Inc()

	if got := counterMetricValue(t, r, "fractional_total", nil); got != 2.75 {
		t.Fatalf("counter value = %v, want 2.75", got)
	}
}

func TestCounterVecFractionalAccumulationByLabels(t *testing.T) {
	r := NewRegistry("")
	c := r.Counter("labeled_fractional_total", "Fractional counter", "method")

	c.With(attr.String("method", "GET")).Add(0.25)
	c.With(attr.String("method", "GET")).Add(0.5)
	c.With(attr.String("method", "POST")).Add(1.25)

	if got := counterMetricValue(t, r, "labeled_fractional_total", map[string]string{"method": "GET"}); got != 0.75 {
		t.Fatalf("GET counter value = %v, want 0.75", got)
	}
	if got := counterMetricValue(t, r, "labeled_fractional_total", map[string]string{"method": "POST"}); got != 1.25 {
		t.Fatalf("POST counter value = %v, want 1.25", got)
	}
}

func TestCounterRejectsNegativeAndNaNValues(t *testing.T) {
	r := NewRegistry("")
	c := r.Counter("validated_total", "Validated counter", "kind")
	valid := c.With(attr.String("kind", "valid"))

	valid.Add(2.5)
	valid.Add(-1)
	valid.Add(math.NaN())
	c.Add(-1)
	c.Add(math.NaN())

	if got := counterMetricValue(t, r, "validated_total", map[string]string{"kind": "valid"}); got != 2.5 {
		t.Fatalf("counter value after rejected updates = %v, want 2.5", got)
	}
	if got := len(r.Gather()[0].Metrics); got != 1 {
		t.Fatalf("rejected Counter.Add created a metric series: got %d series, want 1", got)
	}
}

func TestCounterConcurrentFractionalAdds(t *testing.T) {
	r := NewRegistry("")
	c := r.Counter("concurrent_fractional_total", "Concurrent fractional counter")
	vec := c.With()

	const (
		goroutines = 32
		adds       = 1000
		delta      = 0.25
	)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range adds {
				vec.Add(delta)
			}
		}()
	}
	wg.Wait()

	want := float64(goroutines * adds * delta)
	if got := counterMetricValue(t, r, "concurrent_fractional_total", nil); got != want {
		t.Fatalf("counter value = %v, want %v", got, want)
	}
}

func counterMetricValue(t *testing.T, r *Registry, name string, labels map[string]string) float64 {
	t.Helper()
	for _, family := range r.Gather() {
		if family.Name != name {
			continue
		}
		for _, metric := range family.Metrics {
			if metricHasLabels(metric, labels) {
				return metric.Value
			}
		}
	}
	t.Fatalf("metric %q with labels %v not found", name, labels)
	return 0
}

func metricHasLabels(metric Metric, labels map[string]string) bool {
	if metric.Labels.Len() != len(labels) {
		return false
	}
	for key, want := range labels {
		value, ok := metric.Labels.Get(key)
		if !ok || value.String() != want {
			return false
		}
	}
	return true
}
