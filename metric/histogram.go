package metric

import (
	"math"
	"sync"
	"sync/atomic"

	"github.com/kzs0/bedrock/attr"
)

// Histogram observes values and counts them in configurable buckets.
type Histogram struct {
	name       string
	help       string
	buckets    []float64
	labelNames map[string]struct{}
	mu         sync.RWMutex
	values     map[string]*histogramValue
}

type histogramValue struct {
	labels      attr.Set
	bucketCount []atomic.Uint64 // count for each bucket
	count       atomic.Uint64   // total count
	sumBits     atomic.Uint64   // sum stored as float64 bits
}

// With returns a HistogramVec with the given label values.
func (h *Histogram) With(labels ...attr.Attr) *HistogramVec {
	labels_verified := make([]attr.Attr, 0, len(labels))
	for _, label := range labels {
		sanitized := sanitizeName(label.Key)
		if _, ok := h.labelNames[sanitized]; !ok {
			continue
		}
		label = label.WithKey(sanitized)
		labels_verified = append(labels_verified, label)
	}

	key := labelsKey(labels_verified)

	h.mu.RLock()
	hv, ok := h.values[key]
	h.mu.RUnlock()

	if ok {
		return &HistogramVec{value: hv, buckets: h.buckets}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Double-check after acquiring write lock
	if hv, ok = h.values[key]; ok {
		return &HistogramVec{value: hv, buckets: h.buckets}
	}

	hv = &histogramValue{
		labels:      attr.NewSet(labels_verified...),
		bucketCount: make([]atomic.Uint64, len(h.buckets)),
	}
	h.values[key] = hv
	return &HistogramVec{value: hv, buckets: h.buckets}
}

// Observe adds a single observation to the histogram.
func (h *Histogram) Observe(v float64) {
	h.With().Observe(v)
}

// collect gathers all histogram values for exposition.
func (h *Histogram) collect() MetricFamily {
	h.mu.RLock()
	defer h.mu.RUnlock()

	metrics := make([]Metric, 0, len(h.values))
	for _, hv := range h.values {
		buckets := make([]Bucket, len(h.buckets))
		var cumulative uint64
		for i, bound := range h.buckets {
			cumulative += hv.bucketCount[i].Load()
			buckets[i] = Bucket{
				UpperBound: bound,
				Count:      cumulative,
			}
		}

		metrics = append(metrics, Metric{
			Labels:  hv.labels,
			Buckets: buckets,
			Count:   hv.count.Load(),
			Sum:     math.Float64frombits(hv.sumBits.Load()),
		})
	}

	return MetricFamily{
		Name:    h.name,
		Help:    h.help,
		Type:    TypeHistogram,
		Metrics: metrics,
	}
}

// HistogramVec is a histogram with specific label values.
type HistogramVec struct {
	value   *histogramValue
	buckets []float64
}

// Observe adds a single observation to the histogram.
func (hv *HistogramVec) Observe(v float64) {
	// Increment count
	hv.value.count.Add(1)

	// Add to sum using CAS loop
	for {
		oldBits := hv.value.sumBits.Load()
		newSum := math.Float64frombits(oldBits) + v
		if hv.value.sumBits.CompareAndSwap(oldBits, math.Float64bits(newSum)) {
			break
		}
	}

	// Increment appropriate bucket(s)
	for i, bound := range hv.buckets {
		if v <= bound {
			hv.value.bucketCount[i].Add(1)
			return
		}
	}
	// Value is larger than all buckets, goes in +Inf (counted in count but not buckets)
}
