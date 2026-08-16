package bedrock

import (
	"context"
	"reflect"
	"testing"

	"github.com/kzs0/bedrock/metric"
)

func TestConfiguredMetricBucketsApplyToAllDefaultHistograms(t *testing.T) {
	configured := []float64{7, 13, 29}
	b, err := New(Config{Service: "test", MetricBuckets: configured})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx := WithBedrock(context.Background(), b)

	Histogram(ctx, "api_histogram", "API histogram", nil).Observe(10)
	op, _ := Operation(ctx, "operation")
	op.Done()
	source, sourceCtx := Source(ctx, "worker")
	source.Histogram(sourceCtx, "latency_ms", 10)

	// New must isolate the live registry from later Config slice mutations.
	configured[0] = 999

	families := b.Metrics().Gather()
	for _, name := range []string{
		"api_histogram",
		"operation_duration_ms",
		"worker_latency_ms",
	} {
		if got := histogramBounds(t, families, name); !reflect.DeepEqual(got, []float64{7, 13, 29}) {
			t.Errorf("%s buckets = %v, want [7 13 29]", name, got)
		}
	}
}

func TestExplicitHistogramBucketsOverrideConfig(t *testing.T) {
	b, err := New(Config{Service: "test", MetricBuckets: []float64{7, 13, 29}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx := WithBedrock(context.Background(), b)

	explicit := []float64{1, 2}
	Histogram(ctx, "explicit_histogram", "Explicit histogram", explicit).Observe(1.5)
	explicit[0] = 999

	got := histogramBounds(t, b.Metrics().Gather(), "explicit_histogram")
	if !reflect.DeepEqual(got, []float64{1, 2}) {
		t.Fatalf("explicit histogram buckets = %v, want [1 2]", got)
	}
}

func histogramBounds(t *testing.T, families []metric.MetricFamily, name string) []float64 {
	t.Helper()
	for _, family := range families {
		if family.Name != name {
			continue
		}
		if len(family.Metrics) != 1 {
			t.Fatalf("%s: expected 1 metric, got %d", name, len(family.Metrics))
		}
		bounds := make([]float64, len(family.Metrics[0].Buckets))
		for i, bucket := range family.Metrics[0].Buckets {
			bounds[i] = bucket.UpperBound
		}
		return bounds
	}
	t.Fatalf("histogram %q not found", name)
	return nil
}
