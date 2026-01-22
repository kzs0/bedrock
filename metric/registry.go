package metric

import (
	"strings"
	"sync"

	"github.com/kzs0/bedrock/attr"
)

// Registry is a thread-safe registry for metrics.
type Registry struct {
	mu         sync.RWMutex
	prefix     string
	counters   map[string]*Counter
	gauges     map[string]*Gauge
	histograms map[string]*Histogram
}

// NewRegistry creates a new metric registry with an optional prefix.
// The prefix is prepended to all metric names (e.g., prefix="myapp" creates "myapp_metric_name").
// If prefix is empty, no prefix is added.
func NewRegistry(prefix string) *Registry {
	return &Registry{
		prefix:     prefix,
		counters:   make(map[string]*Counter),
		gauges:     make(map[string]*Gauge),
		histograms: make(map[string]*Histogram),
	}
}

// Counter returns or creates a counter with the given name.
func (r *Registry) Counter(name, help string, labelNames ...string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Prepend prefix if configured
	if r.prefix != "" {
		name = r.prefix + "_" + name
	}

	// Sanitize metric name for Prometheus compatibility
	name = sanitizeName(name)

	if c, ok := r.counters[name]; ok {
		return c
	}

	// Sanitize label names
	sanitizedLabels := make(map[string]struct{}, len(labelNames))
	for _, label := range labelNames {
		sanitizedLabels[sanitizeName(label)] = struct{}{}
	}

	c := &Counter{
		name:       name,
		help:       help,
		labelNames: sanitizedLabels,
		values:     make(map[string]*counterValue),
	}
	r.counters[name] = c
	return c
}

// Gauge returns or creates a gauge with the given name.
func (r *Registry) Gauge(name, help string, labelNames ...string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Prepend prefix if configured
	if r.prefix != "" {
		name = r.prefix + "_" + name
	}

	// Sanitize metric name for Prometheus compatibility
	name = sanitizeName(name)

	if g, ok := r.gauges[name]; ok {
		return g
	}

	// Sanitize label names
	sanitizedLabels := make(map[string]struct{}, len(labelNames))
	for _, label := range labelNames {
		sanitizedLabels[sanitizeName(label)] = struct{}{}
	}

	g := &Gauge{
		name:       name,
		help:       help,
		labelNames: sanitizedLabels,
		values:     make(map[string]*gaugeValue),
	}
	r.gauges[name] = g
	return g
}

// Histogram returns or creates a histogram with the given name.
func (r *Registry) Histogram(name, help string, buckets []float64, labelNames ...string) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Prepend prefix if configured
	if r.prefix != "" {
		name = r.prefix + "_" + name
	}

	// Sanitize metric name for Prometheus compatibility
	name = sanitizeName(name)

	if h, ok := r.histograms[name]; ok {
		return h
	}

	if len(buckets) == 0 {
		buckets = DefaultBuckets
	}

	// Sanitize label names
	sanitizedLabels := make(map[string]struct{}, len(labelNames))
	for _, label := range labelNames {
		sanitizedLabels[sanitizeName(label)] = struct{}{}
	}

	h := &Histogram{
		name:       name,
		help:       help,
		buckets:    buckets,
		labelNames: sanitizedLabels,
		values:     make(map[string]*histogramValue),
	}
	r.histograms[name] = h
	return h
}

// Gather collects all metrics for exposition.
func (r *Registry) Gather() []MetricFamily {
	r.mu.RLock()
	defer r.mu.RUnlock()

	families := make([]MetricFamily, 0, len(r.counters)+len(r.gauges)+len(r.histograms))

	for _, c := range r.counters {
		families = append(families, c.collect())
	}
	for _, g := range r.gauges {
		families = append(families, g.collect())
	}
	for _, h := range r.histograms {
		families = append(families, h.collect())
	}

	return families
}

// MetricFamily represents a collection of metrics with the same name.
type MetricFamily struct {
	Name    string
	Help    string
	Type    MetricType
	Metrics []Metric
}

// MetricType is the type of a metric.
type MetricType string

const (
	TypeCounter   MetricType = "counter"
	TypeGauge     MetricType = "gauge"
	TypeHistogram MetricType = "histogram"
)

// Metric represents a single metric with labels and value(s).
type Metric struct {
	Labels  attr.Set
	Value   float64  // For counter/gauge
	Buckets []Bucket // For histogram
	Count   uint64   // For histogram
	Sum     float64  // For histogram
}

// Bucket represents a histogram bucket.
type Bucket struct {
	UpperBound float64
	Count      uint64
}

// DefaultBuckets are the default histogram buckets.
var DefaultBuckets = []float64{.5, 1, 2.5, 5, 10, 25, 50, 100, 250, 500, 1000}

// sanitizeName converts metric/label names to valid Prometheus names.
// Prometheus metric and label names must match [a-zA-Z_:][a-zA-Z0-9_:]*.
// This replaces dots and other invalid characters with underscores.
func sanitizeName(name string) string {
	// Replace dots with underscores
	name = strings.ReplaceAll(name, ".", "_")
	// Replace any other non-alphanumeric characters (except underscores and colons)
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == ':' {
			return r
		}
		return '_'
	}, name)
}
