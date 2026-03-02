package metric

import (
	"testing"

	"github.com/kzs0/bedrock/attr"
)

// ── Counter.Add negative value ──────────────────────────────────────────────

func TestCounterVec_AddNegative(t *testing.T) {
	r := NewRegistry("")
	c := r.Counter("test_counter_neg", "test")

	c.Inc()
	c.Add(-5) // Should be ignored

	families := r.Gather()
	for _, f := range families {
		if f.Name == "test_counter_neg" && len(f.Metrics) > 0 {
			if f.Metrics[0].Value != 1 {
				t.Errorf("expected 1 (negative add ignored), got %f", f.Metrics[0].Value)
			}
		}
	}
}

// ── Counter.With overflow (>8 labels) ──────────────────────────────────────

func TestCounterWith_ManyLabels(t *testing.T) {
	r := NewRegistry("")
	labels := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	c := r.Counter("many_labels_counter", "test", labels...)

	attrs := make([]attr.Attr, len(labels))
	for i, l := range labels {
		attrs[i] = attr.String(l, "v"+l)
	}
	c.With(attrs...).Inc()

	families := r.Gather()
	for _, f := range families {
		if f.Name == "many_labels_counter" && len(f.Metrics) > 0 {
			if f.Metrics[0].Value != 1 {
				t.Errorf("expected 1, got %f", f.Metrics[0].Value)
			}
		}
	}
}

// ── Gauge.With overflow ────────────────────────────────────────────────────

func TestGaugeWith_ManyLabels(t *testing.T) {
	r := NewRegistry("")
	labels := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	g := r.Gauge("many_labels_gauge", "test", labels...)

	attrs := make([]attr.Attr, len(labels))
	for i, l := range labels {
		attrs[i] = attr.String(l, "v"+l)
	}
	g.With(attrs...).Set(42)

	families := r.Gather()
	for _, f := range families {
		if f.Name == "many_labels_gauge" && len(f.Metrics) > 0 {
			if f.Metrics[0].Value != 42 {
				t.Errorf("expected 42, got %f", f.Metrics[0].Value)
			}
		}
	}
}

// ── Histogram.With overflow ────────────────────────────────────────────────

func TestHistogramWith_ManyLabels(t *testing.T) {
	r := NewRegistry("")
	labels := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	h := r.Histogram("many_labels_hist", "test", nil, labels...)

	attrs := make([]attr.Attr, len(labels))
	for i, l := range labels {
		attrs[i] = attr.String(l, "v"+l)
	}
	h.With(attrs...).Observe(1.5)

	families := r.Gather()
	for _, f := range families {
		if f.Name == "many_labels_hist" && len(f.Metrics) > 0 {
			if f.Metrics[0].Count != 1 {
				t.Errorf("expected count 1, got %d", f.Metrics[0].Count)
			}
		}
	}
}

// ── Counter.With filters unknown labels ─────────────────────────────────────

func TestCounterWith_FiltersUnknownLabels(t *testing.T) {
	r := NewRegistry("")
	c := r.Counter("filter_counter", "test", "known")

	c.With(attr.String("known", "v1"), attr.String("unknown", "v2")).Inc()

	families := r.Gather()
	for _, f := range families {
		if f.Name == "filter_counter" {
			if len(f.Metrics) != 1 {
				t.Fatalf("expected 1 metric, got %d", len(f.Metrics))
			}
			// Only "known" label should be present
			if f.Metrics[0].Labels.Len() != 1 {
				t.Errorf("expected 1 label, got %d", f.Metrics[0].Labels.Len())
			}
		}
	}
}

// ── Gauge.With filters unknown labels ───────────────────────────────────────

func TestGaugeWith_FiltersUnknownLabels(t *testing.T) {
	r := NewRegistry("")
	g := r.Gauge("filter_gauge", "test", "known")

	g.With(attr.String("known", "v1"), attr.String("unknown", "v2")).Set(5)

	families := r.Gather()
	for _, f := range families {
		if f.Name == "filter_gauge" {
			if len(f.Metrics) != 1 {
				t.Fatalf("expected 1 metric, got %d", len(f.Metrics))
			}
			if f.Metrics[0].Labels.Len() != 1 {
				t.Errorf("expected 1 label, got %d", f.Metrics[0].Labels.Len())
			}
		}
	}
}

// ── Histogram.With filters unknown labels ──────────────────────────────────

func TestHistogramWith_FiltersUnknownLabels(t *testing.T) {
	r := NewRegistry("")
	h := r.Histogram("filter_hist", "test", nil, "known")

	h.With(attr.String("known", "v1"), attr.String("unknown", "v2")).Observe(1)

	families := r.Gather()
	for _, f := range families {
		if f.Name == "filter_hist" {
			if len(f.Metrics) != 1 {
				t.Fatalf("expected 1 metric, got %d", len(f.Metrics))
			}
			if f.Metrics[0].Labels.Len() != 1 {
				t.Errorf("expected 1 label, got %d", f.Metrics[0].Labels.Len())
			}
		}
	}
}

// ── Counter.With concurrent access ─────────────────────────────────────────

func TestCounterWith_Concurrent(t *testing.T) {
	r := NewRegistry("")
	c := r.Counter("concurrent_counter", "test", "key")

	done := make(chan struct{})
	for i := 0; i < 100; i++ {
		go func() {
			c.With(attr.String("key", "v1")).Inc()
			done <- struct{}{}
		}()
	}
	for i := 0; i < 100; i++ {
		<-done
	}

	families := r.Gather()
	for _, f := range families {
		if f.Name == "concurrent_counter" {
			if f.Metrics[0].Value != 100 {
				t.Errorf("expected 100, got %f", f.Metrics[0].Value)
			}
		}
	}
}

// ── Gauge.With concurrent access ────────────────────────────────────────────

func TestGaugeWith_Concurrent(t *testing.T) {
	r := NewRegistry("")
	g := r.Gauge("concurrent_gauge", "test", "key")

	done := make(chan struct{})
	for i := 0; i < 100; i++ {
		go func() {
			g.With(attr.String("key", "v1")).Inc()
			done <- struct{}{}
		}()
	}
	for i := 0; i < 100; i++ {
		<-done
	}

	families := r.Gather()
	for _, f := range families {
		if f.Name == "concurrent_gauge" {
			if f.Metrics[0].Value != 100 {
				t.Errorf("expected 100, got %f", f.Metrics[0].Value)
			}
		}
	}
}

// ── Histogram.With concurrent access ───────────────────────────────────────

func TestHistogramWith_Concurrent(t *testing.T) {
	r := NewRegistry("")
	h := r.Histogram("concurrent_hist", "test", nil, "key")

	done := make(chan struct{})
	for i := 0; i < 100; i++ {
		go func() {
			h.With(attr.String("key", "v1")).Observe(1.0)
			done <- struct{}{}
		}()
	}
	for i := 0; i < 100; i++ {
		<-done
	}

	families := r.Gather()
	for _, f := range families {
		if f.Name == "concurrent_hist" {
			if f.Metrics[0].Count != 100 {
				t.Errorf("expected count 100, got %d", f.Metrics[0].Count)
			}
		}
	}
}
