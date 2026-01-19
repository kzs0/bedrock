package metric

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/kzs0/bedrock/attr"
)

// Counter is a cumulative metric that only goes up.
type Counter struct {
	name       string
	help       string
	labelNames map[string]struct{}
	mu         sync.RWMutex
	values     map[string]*counterValue
}

type counterValue struct {
	labels attr.Set
	value  atomic.Uint64
}

// With returns a CounterVec with the given label values.
func (c *Counter) With(labels ...attr.Attr) *CounterVec {
	labels_verified := make([]attr.Attr, 0, len(labels))
	for _, label := range labels {
		sanitized := sanitizeName(label.Key)
		if _, ok := c.labelNames[sanitized]; !ok {
			continue
		}
		label = label.WithKey(sanitized)
		labels_verified = append(labels_verified, label)
	}

	key := labelsKey(labels_verified)

	c.mu.RLock()
	cv, ok := c.values[key]
	c.mu.RUnlock()

	if ok {
		return &CounterVec{value: cv}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if cv, ok = c.values[key]; ok {
		return &CounterVec{value: cv}
	}

	cv = &counterValue{
		labels: attr.NewSet(labels_verified...),
	}
	c.values[key] = cv
	return &CounterVec{value: cv}
}

// Inc increments the counter by 1.
func (c *Counter) Inc() {
	c.With().Inc()
}

// Add adds the given value to the counter.
func (c *Counter) Add(v float64) {
	c.With().Add(v)
}

// collect gathers all counter values for exposition.
func (c *Counter) collect() MetricFamily {
	c.mu.RLock()
	defer c.mu.RUnlock()

	metrics := make([]Metric, 0, len(c.values))
	for _, cv := range c.values {
		metrics = append(metrics, Metric{
			Labels: cv.labels,
			Value:  float64FromUint64(cv.value.Load()),
		})
	}

	return MetricFamily{
		Name:    c.name,
		Help:    c.help,
		Type:    TypeCounter,
		Metrics: metrics,
	}
}

// CounterVec is a counter with specific label values.
type CounterVec struct {
	value *counterValue
}

// Inc increments the counter by 1.
func (cv *CounterVec) Inc() {
	cv.value.value.Add(1)
}

// Add adds the given value to the counter.
func (cv *CounterVec) Add(v float64) {
	if v < 0 {
		return // Counters can only increase
	}
	// Store as uint64 bits for atomic operations
	cv.value.value.Add(uint64(v))
}

// labelsKey creates a unique key from label values.
func labelsKey(labels []attr.Attr) string {
	if len(labels) == 0 {
		return ""
	}
	set := attr.NewSet(labels...)
	var sb strings.Builder
	set.Range(func(a attr.Attr) bool {
		if sb.Len() > 0 {
			sb.WriteByte('|')
		}
		sb.WriteString(a.Key)
		sb.WriteByte('=')
		sb.WriteString(a.Value.String())
		return true
	})
	return sb.String()
}

// float64FromUint64 converts a uint64 to float64.
func float64FromUint64(v uint64) float64 {
	return float64(v)
}
