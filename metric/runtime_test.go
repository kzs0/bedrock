package metric

import (
	"runtime"
	"testing"

	"github.com/kzs0/bedrock/attr"
)

func TestRuntimeCollector(t *testing.T) {
	r := NewRegistry("")
	collector := NewRuntimeCollector(r)

	// Trigger collection
	collector.Collect()

	// Gather metrics
	families := r.Gather()

	// Verify we have metrics
	if len(families) < 10 {
		t.Errorf("expected at least 10 metric families, got %d", len(families))
	}

	// Check for specific expected metrics
	expectedMetrics := map[string]bool{
		"go_info":                         false,
		"go_goroutines":                   false,
		"go_memstats_heap_alloc_bytes":    false,
		"go_memstats_heap_inuse_bytes":    false,
		"go_memstats_heap_objects":        false,
		"go_memstats_stack_inuse_bytes":   false,
		"go_memstats_mallocs_total":       false,
		"go_memstats_frees_total":         false,
		"go_gc_cycles_total":              false,
		"go_gc_duration_seconds_total":    false,
	}

	for _, fam := range families {
		if _, ok := expectedMetrics[fam.Name]; ok {
			expectedMetrics[fam.Name] = true
		}
	}

	for name, found := range expectedMetrics {
		if !found {
			t.Errorf("expected metric %q not found", name)
		}
	}
}

func TestRuntimeCollectorWithStaticLabels(t *testing.T) {
	r := NewRegistry("")
	staticLabels := []attr.Attr{
		attr.String("env", "test"),
		attr.String("service", "myapp"),
	}
	collector := NewRuntimeCollector(r, staticLabels...)

	// Trigger collection
	collector.Collect()

	// Gather metrics
	families := r.Gather()

	// Find go_goroutines metric and verify static labels
	var goroutinesFamily *MetricFamily
	for i := range families {
		if families[i].Name == "go_goroutines" {
			goroutinesFamily = &families[i]
			break
		}
	}

	if goroutinesFamily == nil {
		t.Fatal("expected go_goroutines metric")
	}

	if len(goroutinesFamily.Metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(goroutinesFamily.Metrics))
	}

	labels := goroutinesFamily.Metrics[0].Labels

	// Verify static labels are present
	envVal, ok := labels.Get("env")
	if !ok || envVal.AsString() != "test" {
		t.Errorf("expected env=test label, got %v", envVal)
	}

	serviceVal, ok := labels.Get("service")
	if !ok || serviceVal.AsString() != "myapp" {
		t.Errorf("expected service=myapp label, got %v", serviceVal)
	}
}

func TestRuntimeCollectorGoroutineCount(t *testing.T) {
	r := NewRegistry("")
	collector := NewRuntimeCollector(r)

	collector.Collect()

	families := r.Gather()

	var goroutinesFamily *MetricFamily
	for i := range families {
		if families[i].Name == "go_goroutines" {
			goroutinesFamily = &families[i]
			break
		}
	}

	if goroutinesFamily == nil {
		t.Fatal("expected go_goroutines metric")
	}

	if goroutinesFamily.Type != TypeGauge {
		t.Errorf("expected gauge type, got %v", goroutinesFamily.Type)
	}

	value := goroutinesFamily.Metrics[0].Value
	actualGoroutines := runtime.NumGoroutine()

	// Allow some tolerance since goroutines can change
	if value < 1 || value > float64(actualGoroutines+10) {
		t.Errorf("goroutine count %f seems unreasonable (actual: %d)", value, actualGoroutines)
	}
}

func TestRuntimeCollectorGoInfo(t *testing.T) {
	r := NewRegistry("")
	collector := NewRuntimeCollector(r)

	collector.Collect()

	families := r.Gather()

	var goInfoFamily *MetricFamily
	for i := range families {
		if families[i].Name == "go_info" {
			goInfoFamily = &families[i]
			break
		}
	}

	if goInfoFamily == nil {
		t.Fatal("expected go_info metric")
	}

	if len(goInfoFamily.Metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(goInfoFamily.Metrics))
	}

	// go_info should have a version label
	versionVal, ok := goInfoFamily.Metrics[0].Labels.Get("version")
	if !ok {
		t.Error("expected version label on go_info metric")
	}

	if versionVal.AsString() != runtime.Version() {
		t.Errorf("expected version %q, got %q", runtime.Version(), versionVal.AsString())
	}

	// Value should be 1
	if goInfoFamily.Metrics[0].Value != 1 {
		t.Errorf("expected go_info value 1, got %f", goInfoFamily.Metrics[0].Value)
	}
}

func TestRuntimeCollectorRegisteredWithRegistry(t *testing.T) {
	r := NewRegistry("")
	collector := NewRuntimeCollector(r)
	r.RegisterCollector(collector)

	// Gather should automatically call Collect
	families := r.Gather()

	// Verify we got runtime metrics
	found := false
	for _, fam := range families {
		if fam.Name == "go_goroutines" {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected go_goroutines metric after Gather (collector should be called)")
	}
}

func TestSanitizeRuntimeMetricName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"/gc/heap/allocs:bytes", "go_runtime__gc_heap_allocs_bytes"},
		{"/memory/classes/heap/free:bytes", "go_runtime__memory_classes_heap_free_bytes"},
		{"/sched/goroutines:goroutines", "go_runtime__sched_goroutines_goroutines"},
		{"simple-name", "go_runtime_simple_name"},
	}

	for _, test := range tests {
		result := sanitizeRuntimeMetricName(test.input)
		if result != test.expected {
			t.Errorf("sanitizeRuntimeMetricName(%q) = %q, expected %q", test.input, result, test.expected)
		}
	}
}

func TestRegistryCollectorInterface(t *testing.T) {
	r := NewRegistry("")

	// Create a mock collector
	collected := false
	mock := &mockCollector{collectFunc: func() { collected = true }}

	r.RegisterCollector(mock)

	// Gather should trigger the collector
	_ = r.Gather()

	if !collected {
		t.Error("collector was not called during Gather")
	}
}

type mockCollector struct {
	collectFunc func()
}

func (m *mockCollector) Collect() {
	m.collectFunc()
}
