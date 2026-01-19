package metric

import (
	"testing"

	"github.com/kzs0/bedrock/attr"
)

func TestCounter(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("requests_total", "Total requests")

	c.Inc()
	c.Inc()
	c.Add(3)

	families := r.Gather()
	if len(families) != 1 {
		t.Fatalf("expected 1 family, got %d", len(families))
	}

	fam := families[0]
	if fam.Name != "requests_total" {
		t.Errorf("expected name 'requests_total', got %q", fam.Name)
	}
	if fam.Type != TypeCounter {
		t.Errorf("expected type counter, got %v", fam.Type)
	}
	if len(fam.Metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(fam.Metrics))
	}
	if fam.Metrics[0].Value != 5 {
		t.Errorf("expected value 5, got %f", fam.Metrics[0].Value)
	}
}

func TestCounterWithLabels(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("http_requests_total", "HTTP requests", "method", "status")

	c.With(attr.String("method", "GET"), attr.String("status", "200")).Inc()
	c.With(attr.String("method", "GET"), attr.String("status", "200")).Inc()
	c.With(attr.String("method", "POST"), attr.String("status", "201")).Inc()

	families := r.Gather()
	if len(families) != 1 {
		t.Fatalf("expected 1 family, got %d", len(families))
	}

	fam := families[0]
	if len(fam.Metrics) != 2 {
		t.Errorf("expected 2 metrics (different label combos), got %d", len(fam.Metrics))
	}
}

func TestGauge(t *testing.T) {
	r := NewRegistry()
	g := r.Gauge("temperature", "Current temperature")

	g.Set(20.5)

	families := r.Gather()
	fam := families[0]
	if fam.Metrics[0].Value != 20.5 {
		t.Errorf("expected value 20.5, got %f", fam.Metrics[0].Value)
	}

	g.Inc()
	families = r.Gather()
	if families[0].Metrics[0].Value != 21.5 {
		t.Errorf("expected value 21.5 after inc, got %f", families[0].Metrics[0].Value)
	}

	g.Dec()
	families = r.Gather()
	if families[0].Metrics[0].Value != 20.5 {
		t.Errorf("expected value 20.5 after dec, got %f", families[0].Metrics[0].Value)
	}

	g.Add(5)
	families = r.Gather()
	if families[0].Metrics[0].Value != 25.5 {
		t.Errorf("expected value 25.5 after add, got %f", families[0].Metrics[0].Value)
	}

	g.Sub(10)
	families = r.Gather()
	if families[0].Metrics[0].Value != 15.5 {
		t.Errorf("expected value 15.5 after sub, got %f", families[0].Metrics[0].Value)
	}
}

func TestHistogram(t *testing.T) {
	r := NewRegistry()
	h := r.Histogram("request_duration", "Request duration", []float64{0.1, 0.5, 1.0})

	h.Observe(0.05) // bucket 0.1
	h.Observe(0.3)  // bucket 0.5
	h.Observe(0.7)  // bucket 1.0
	h.Observe(0.08) // bucket 0.1
	h.Observe(2.0)  // +Inf

	families := r.Gather()
	if len(families) != 1 {
		t.Fatalf("expected 1 family, got %d", len(families))
	}

	fam := families[0]
	if fam.Type != TypeHistogram {
		t.Errorf("expected type histogram, got %v", fam.Type)
	}

	m := fam.Metrics[0]
	if m.Count != 5 {
		t.Errorf("expected count 5, got %d", m.Count)
	}

	expectedSum := 0.05 + 0.3 + 0.7 + 0.08 + 2.0
	if m.Sum != expectedSum {
		t.Errorf("expected sum %f, got %f", expectedSum, m.Sum)
	}

	// Check buckets (cumulative)
	if len(m.Buckets) != 3 {
		t.Fatalf("expected 3 buckets, got %d", len(m.Buckets))
	}
	if m.Buckets[0].Count != 2 { // <= 0.1
		t.Errorf("expected bucket[0.1] count 2, got %d", m.Buckets[0].Count)
	}
	if m.Buckets[1].Count != 3 { // <= 0.5 (cumulative: 2 + 1)
		t.Errorf("expected bucket[0.5] count 3, got %d", m.Buckets[1].Count)
	}
	if m.Buckets[2].Count != 4 { // <= 1.0 (cumulative: 3 + 1)
		t.Errorf("expected bucket[1.0] count 4, got %d", m.Buckets[2].Count)
	}
}

func TestRegistryGetOrCreate(t *testing.T) {
	r := NewRegistry()

	c1 := r.Counter("test_counter", "Test")
	c2 := r.Counter("test_counter", "Test")

	if c1 != c2 {
		t.Error("expected same counter instance")
	}

	c1.Inc()
	families := r.Gather()
	if families[0].Metrics[0].Value != 1 {
		t.Error("counter should be shared")
	}
}
