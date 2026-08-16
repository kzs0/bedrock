package metric

import (
	"math"
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
	bits   atomic.Uint64 // Stores float64 as uint64 bits
}

// With returns a CounterVec with the given label values.
func (c *Counter) With(labels ...attr.Attr) CounterVec {
	var buf [8]attr.Attr
	verified := buf[:0]
	var overflow []attr.Attr
	for _, label := range labels {
		sanitized := sanitizeName(label.Key)
		if _, ok := c.labelNames[sanitized]; !ok {
			continue
		}
		label = label.WithKey(sanitized)
		if len(verified) < len(buf) {
			verified = verified[:len(verified)+1]
			verified[len(verified)-1] = label
		} else {
			if overflow == nil {
				overflow = make([]attr.Attr, len(verified), len(labels))
				copy(overflow, verified)
			}
			overflow = append(overflow, label)
		}
	}
	if overflow != nil {
		verified = overflow
	}

	key := labelsKey(verified)

	c.mu.RLock()
	cv, ok := c.values[key]
	c.mu.RUnlock()

	if ok {
		return CounterVec{value: cv}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if cv, ok = c.values[key]; ok {
		return CounterVec{value: cv}
	}

	cv = &counterValue{
		labels: attr.NewSet(verified...),
	}
	c.values[key] = cv
	return CounterVec{value: cv}
}

// Inc increments the counter by 1.
func (c *Counter) Inc() {
	c.With().Inc()
}

// Add adds the given value to the counter.
func (c *Counter) Add(v float64) {
	if v < 0 || math.IsNaN(v) {
		return
	}
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
			Value:  math.Float64frombits(cv.bits.Load()),
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
func (cv CounterVec) Inc() {
	cv.add(1)
}

// Add adds the given value to the counter.
func (cv CounterVec) Add(v float64) {
	if v < 0 || math.IsNaN(v) {
		return
	}
	cv.add(v)
}

func (cv CounterVec) add(delta float64) {
	for {
		oldBits := cv.value.bits.Load()
		newValue := math.Float64frombits(oldBits) + delta
		if cv.value.bits.CompareAndSwap(oldBits, math.Float64bits(newValue)) {
			return
		}
	}
}

// labelsKey creates a unique key from label values.
// Sorts in-place using insertion sort (no alloc; labels slice is caller-local).
func labelsKey(labels []attr.Attr) string {
	if len(labels) == 0 {
		return ""
	}
	// Insertion sort for canonical key ordering — avoids closure alloc of sort.Slice.
	for i := 1; i < len(labels); i++ {
		for j := i; j > 0 && labels[j-1].Key > labels[j].Key; j-- {
			labels[j-1], labels[j] = labels[j], labels[j-1]
		}
	}
	var sb strings.Builder
	for i, a := range labels {
		if i > 0 {
			sb.WriteByte('|')
		}
		sb.WriteString(a.Key)
		sb.WriteByte('=')
		sb.WriteString(a.Value.String())
	}
	return sb.String()
}
