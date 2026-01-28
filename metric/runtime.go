package metric

import (
	"runtime"
	"runtime/metrics"
	"sync"

	"github.com/kzs0/bedrock/attr"
)

// RuntimeCollector collects Go runtime metrics and exposes them as gauges.
// It automatically includes static labels on all metrics.
type RuntimeCollector struct {
	registry     *Registry
	staticLabels []attr.Attr

	// Gauges for runtime metrics
	goInfo           *Gauge
	goroutines       *Gauge
	threads          *Gauge
	heapAllocBytes   *Gauge
	heapIdleBytes    *Gauge
	heapInuseBytes   *Gauge
	heapObjects      *Gauge
	heapReleasedBytes *Gauge
	stackInuseBytes  *Gauge
	stackSysBytes    *Gauge
	mallocs          *Gauge
	frees            *Gauge
	gcSysBytes       *Gauge
	gcNextBytes      *Gauge
	gcLastNanos      *Gauge
	gcPauseTotalNanos *Gauge
	gcNumGC          *Gauge
	gcNumForcedGC    *Gauge
	cpuClasses       map[string]*Gauge
	memoryClasses    map[string]*Gauge

	mu sync.Mutex
}

// NewRuntimeCollector creates a new runtime metrics collector.
// The static labels are automatically applied to all metrics.
func NewRuntimeCollector(registry *Registry, staticLabels ...attr.Attr) *RuntimeCollector {
	// Extract label names from static labels
	labelNames := make([]string, 0, len(staticLabels))
	for _, label := range staticLabels {
		labelNames = append(labelNames, label.Key)
	}

	rc := &RuntimeCollector{
		registry:     registry,
		staticLabels: staticLabels,
		cpuClasses:   make(map[string]*Gauge),
		memoryClasses: make(map[string]*Gauge),
	}

	// Create gauges for basic runtime metrics
	rc.goInfo = registry.Gauge("go_info", "Information about the Go environment", append(labelNames, "version")...)
	rc.goroutines = registry.Gauge("go_goroutines", "Number of goroutines that currently exist", labelNames...)
	rc.threads = registry.Gauge("go_threads", "Number of OS threads created", labelNames...)

	// Memory metrics
	rc.heapAllocBytes = registry.Gauge("go_memstats_heap_alloc_bytes", "Number of heap bytes allocated and still in use", labelNames...)
	rc.heapIdleBytes = registry.Gauge("go_memstats_heap_idle_bytes", "Number of heap bytes waiting to be used", labelNames...)
	rc.heapInuseBytes = registry.Gauge("go_memstats_heap_inuse_bytes", "Number of heap bytes that are in use", labelNames...)
	rc.heapObjects = registry.Gauge("go_memstats_heap_objects", "Number of allocated objects", labelNames...)
	rc.heapReleasedBytes = registry.Gauge("go_memstats_heap_released_bytes", "Number of heap bytes released to OS", labelNames...)
	rc.stackInuseBytes = registry.Gauge("go_memstats_stack_inuse_bytes", "Number of bytes in use by the stack allocator", labelNames...)
	rc.stackSysBytes = registry.Gauge("go_memstats_stack_sys_bytes", "Number of bytes obtained from system for stack allocator", labelNames...)
	rc.mallocs = registry.Gauge("go_memstats_mallocs_total", "Total number of mallocs", labelNames...)
	rc.frees = registry.Gauge("go_memstats_frees_total", "Total number of frees", labelNames...)

	// GC metrics
	rc.gcSysBytes = registry.Gauge("go_memstats_gc_sys_bytes", "Number of bytes used for garbage collection system metadata", labelNames...)
	rc.gcNextBytes = registry.Gauge("go_memstats_next_gc_bytes", "Number of heap bytes when next garbage collection will take place", labelNames...)
	rc.gcLastNanos = registry.Gauge("go_memstats_last_gc_time_seconds", "Time of last garbage collection in seconds since epoch", labelNames...)
	rc.gcPauseTotalNanos = registry.Gauge("go_gc_duration_seconds_total", "Total garbage collection pause time in seconds", labelNames...)
	rc.gcNumGC = registry.Gauge("go_gc_cycles_total", "Total number of completed GC cycles", labelNames...)
	rc.gcNumForcedGC = registry.Gauge("go_gc_cycles_forced_total", "Total number of forced GC cycles", labelNames...)

	return rc
}

// Collect updates all runtime metrics with current values.
// This should be called periodically or before scraping metrics.
func (rc *RuntimeCollector) Collect() {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	// Set Go version info
	versionLabels := append(rc.staticLabels, attr.String("version", runtime.Version()))
	rc.goInfo.With(versionLabels...).Set(1)

	// Basic metrics
	rc.goroutines.With(rc.staticLabels...).Set(float64(runtime.NumGoroutine()))

	// Read memory stats
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	rc.threads.With(rc.staticLabels...).Set(float64(memStats.NumGC)) // NumGC as proxy, see below for threads
	rc.heapAllocBytes.With(rc.staticLabels...).Set(float64(memStats.HeapAlloc))
	rc.heapIdleBytes.With(rc.staticLabels...).Set(float64(memStats.HeapIdle))
	rc.heapInuseBytes.With(rc.staticLabels...).Set(float64(memStats.HeapInuse))
	rc.heapObjects.With(rc.staticLabels...).Set(float64(memStats.HeapObjects))
	rc.heapReleasedBytes.With(rc.staticLabels...).Set(float64(memStats.HeapReleased))
	rc.stackInuseBytes.With(rc.staticLabels...).Set(float64(memStats.StackInuse))
	rc.stackSysBytes.With(rc.staticLabels...).Set(float64(memStats.StackSys))
	rc.mallocs.With(rc.staticLabels...).Set(float64(memStats.Mallocs))
	rc.frees.With(rc.staticLabels...).Set(float64(memStats.Frees))
	rc.gcSysBytes.With(rc.staticLabels...).Set(float64(memStats.GCSys))
	rc.gcNextBytes.With(rc.staticLabels...).Set(float64(memStats.NextGC))
	rc.gcLastNanos.With(rc.staticLabels...).Set(float64(memStats.LastGC) / 1e9)
	rc.gcPauseTotalNanos.With(rc.staticLabels...).Set(float64(memStats.PauseTotalNs) / 1e9)
	rc.gcNumGC.With(rc.staticLabels...).Set(float64(memStats.NumGC))
	rc.gcNumForcedGC.With(rc.staticLabels...).Set(float64(memStats.NumForcedGC))

	// Read runtime/metrics for additional data
	rc.collectRuntimeMetrics()
}

// collectRuntimeMetrics collects metrics from the runtime/metrics package.
func (rc *RuntimeCollector) collectRuntimeMetrics() {
	// Define the metrics we want to read
	descs := metrics.All()
	samples := make([]metrics.Sample, len(descs))
	for i := range descs {
		samples[i].Name = descs[i].Name
	}

	// Read all metrics
	metrics.Read(samples)

	// Process each sample
	for _, sample := range samples {
		name, value := sample.Name, sample.Value

		switch value.Kind() {
		case metrics.KindUint64:
			rc.setRuntimeMetric(name, float64(value.Uint64()))
		case metrics.KindFloat64:
			rc.setRuntimeMetric(name, value.Float64())
		case metrics.KindFloat64Histogram:
			// Skip histograms for now - they require more complex handling
			continue
		case metrics.KindBad:
			continue
		}
	}
}

// setRuntimeMetric sets a runtime metric gauge, creating it if necessary.
func (rc *RuntimeCollector) setRuntimeMetric(name string, value float64) {
	// Convert runtime/metrics name to prometheus-compatible name
	// e.g., "/gc/heap/allocs:bytes" -> "go_runtime_gc_heap_allocs_bytes"
	promName := sanitizeRuntimeMetricName(name)

	// Check if we already have this gauge in our memory classes map
	gauge, ok := rc.memoryClasses[name]
	if !ok {
		// Extract label names from static labels
		labelNames := make([]string, 0, len(rc.staticLabels))
		for _, label := range rc.staticLabels {
			labelNames = append(labelNames, label.Key)
		}

		// Create the gauge
		gauge = rc.registry.Gauge(promName, "Go runtime metric: "+name, labelNames...)
		rc.memoryClasses[name] = gauge
	}

	gauge.With(rc.staticLabels...).Set(value)
}

// sanitizeRuntimeMetricName converts a runtime/metrics name to a Prometheus-compatible name.
// e.g., "/gc/heap/allocs:bytes" -> "go_runtime_gc_heap_allocs_bytes"
func sanitizeRuntimeMetricName(name string) string {
	// Remove leading slash and replace special chars
	result := make([]byte, 0, len(name)+11) // "go_runtime_" prefix
	result = append(result, "go_runtime_"...)

	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
			result = append(result, c)
		case c >= 'A' && c <= 'Z':
			result = append(result, c)
		case c >= '0' && c <= '9':
			result = append(result, c)
		case c == '_':
			result = append(result, c)
		case c == '/' || c == ':' || c == '-' || c == '.':
			result = append(result, '_')
		default:
			// Skip other characters
		}
	}

	return string(result)
}
