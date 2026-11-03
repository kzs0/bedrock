package metric

import (
	"math"
	"sync"
	"sync/atomic"

	"github.com/kzs0/bedrock/attr"
)

// Gauge is a metric that can go up and down.
type Gauge struct {
	name       string
	help       string
	labelNames map[string]struct{}
	budget     *seriesBudget
	mu         sync.RWMutex
	values     map[string]*gaugeValue
}

type gaugeValue struct {
	labels attr.Set
	bits   atomic.Uint64 // Stores float64 as uint64 bits
}

// With returns a GaugeVec with the given label values.
func (g *Gauge) With(labels ...attr.Attr) GaugeVec {
	var buf [8]attr.Attr
	verified := buf[:0]
	var overflow []attr.Attr
	for _, label := range labels {
		if g.budget != nil && g.budget.filter != nil {
			var keep bool
			label, keep = g.budget.filter(label)
			if !keep {
				continue
			}
		}
		sanitized := sanitizeName(label.Key)
		if _, ok := g.labelNames[sanitized]; !ok {
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

	verified = attr.NewSet(verified...).Attrs()
	key := labelsKey(verified)

	g.mu.RLock()
	gv, ok := g.values[key]
	g.mu.RUnlock()

	if ok {
		return GaugeVec{value: gv}
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	// Double-check after acquiring write lock
	if gv, ok = g.values[key]; ok {
		return GaugeVec{value: gv}
	}

	if g.budget != nil && len(g.values) >= int(g.budget.limit.Load()) {
		g.budget.dropped.Add(1)
		return GaugeVec{}
	}

	gv = &gaugeValue{
		labels: attr.NewSet(verified...),
	}
	g.values[key] = gv
	return GaugeVec{value: gv}
}

// Set sets the gauge to the given value.
func (g *Gauge) Set(v float64) {
	g.With().Set(v)
}

// Inc increments the gauge by 1.
func (g *Gauge) Inc() {
	g.With().Inc()
}

// Dec decrements the gauge by 1.
func (g *Gauge) Dec() {
	g.With().Dec()
}

// Add adds the given value to the gauge.
func (g *Gauge) Add(v float64) {
	g.With().Add(v)
}

// Sub subtracts the given value from the gauge.
func (g *Gauge) Sub(v float64) {
	g.With().Sub(v)
}

// collect gathers all gauge values for exposition.
func (g *Gauge) collect() MetricFamily {
	g.mu.RLock()
	defer g.mu.RUnlock()

	metrics := make([]Metric, 0, len(g.values))
	for _, gv := range g.values {
		metrics = append(metrics, Metric{
			Labels: gv.labels,
			Value:  math.Float64frombits(gv.bits.Load()),
		})
	}

	return MetricFamily{
		Name:    g.name,
		Help:    g.help,
		Type:    TypeGauge,
		Metrics: metrics,
	}
}

// GaugeVec is a gauge with specific label values.
type GaugeVec struct {
	value *gaugeValue
}

// Set sets the gauge to the given value.
func (gv GaugeVec) Set(v float64) {
	if gv.value == nil {
		return
	}
	gv.value.bits.Store(math.Float64bits(v))
}

// Inc increments the gauge by 1.
func (gv GaugeVec) Inc() {
	if gv.value == nil {
		return
	}
	gv.Add(1)
}

// Dec decrements the gauge by 1.
func (gv GaugeVec) Dec() {
	if gv.value == nil {
		return
	}
	gv.Add(-1)
}

// Add adds the given value to the gauge.
func (gv GaugeVec) Add(delta float64) {
	if gv.value == nil {
		return
	}
	for {
		oldBits := gv.value.bits.Load()
		newVal := math.Float64frombits(oldBits) + delta
		if gv.value.bits.CompareAndSwap(oldBits, math.Float64bits(newVal)) {
			return
		}
	}
}

// Sub subtracts the given value from the gauge.
func (gv GaugeVec) Sub(delta float64) {
	if gv.value == nil {
		return
	}
	gv.Add(-delta)
}
