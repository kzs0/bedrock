package metric

import (
	"github.com/kzs0/bedrock/attr"
	"testing"
)

func TestMetricLabelIdentityEscapesSeparators(t *testing.T) {
	for _, kind := range []string{"counter", "gauge", "histogram"} {
		t.Run(kind, func(t *testing.T) {
			r := NewRegistry("")
			left := []attr.Attr{attr.String("a", "x|b=y"), attr.String("b", "z")}
			right := []attr.Attr{attr.String("a", "x"), attr.String("b", "y|b=z")}
			switch kind {
			case "counter":
				c := r.Counter("m", "", "a", "b")
				c.With(left...).Inc()
				c.With(right...).Inc()
			case "gauge":
				g := r.Gauge("m", "", "a", "b")
				g.With(left...).Set(1)
				g.With(right...).Set(2)
			case "histogram":
				h := r.Histogram("m", "", nil, "a", "b")
				h.With(left...).Observe(1)
				h.With(right...).Observe(2)
			}
			got := r.Gather()
			if len(got) != 1 || len(got[0].Metrics) != 2 {
				t.Fatalf("distinct label sets merged: %+v", got)
			}
		})
	}
}
func TestMetricRegistrationsRejectSchemaConflicts(t *testing.T) {
	for _, kind := range []string{"counter", "gauge", "histogram"} {
		t.Run(kind, func(t *testing.T) {
			r := NewRegistry("")
			register := func(labels ...string) {
				switch kind {
				case "counter":
					r.Counter("m", "", labels...)
				case "gauge":
					r.Gauge("m", "", labels...)
				case "histogram":
					r.Histogram("m", "", nil, labels...)
				}
			}
			register("region")
			register("region")
			defer func() {
				if recover() == nil {
					t.Fatal("conflicting metric schema accepted")
				}
			}()
			register("tenant")
		})
	}
}
func TestSanitizedMetricNameCollisionsAreRejected(t *testing.T) {
	r := NewRegistry("")
	r.Counter("work.count", "")
	defer func() {
		if recover() == nil {
			t.Fatal("sanitized metric collision accepted")
		}
	}()
	r.Counter("work_count", "")
}
