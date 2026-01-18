package trace

import (
	"math/rand"
	"sync"

	"github.com/kzs0/bedrock/internal"
)

// SamplingDecision represents the decision made by a sampler.
type SamplingDecision int

const (
	SamplingDecisionDrop SamplingDecision = iota
	SamplingDecisionRecord
	SamplingDecisionRecordAndSample
)

// SamplingResult contains the result of a sampling decision.
type SamplingResult struct {
	Decision SamplingDecision
}

// Sampler decides whether a span should be sampled.
type Sampler interface {
	ShouldSample(traceID internal.TraceID, name string, parentSampled bool) SamplingResult
}

// AlwaysSampler always samples.
type AlwaysSampler struct{}

// ShouldSample always returns RecordAndSample.
func (AlwaysSampler) ShouldSample(traceID internal.TraceID, name string, parentSampled bool) SamplingResult {
	return SamplingResult{Decision: SamplingDecisionRecordAndSample}
}

// NeverSampler never samples.
type NeverSampler struct{}

// ShouldSample always returns Drop.
func (NeverSampler) ShouldSample(traceID internal.TraceID, name string, parentSampled bool) SamplingResult {
	return SamplingResult{Decision: SamplingDecisionDrop}
}

// RatioSampler samples a fraction of traces.
type RatioSampler struct {
	ratio float64
	mu    sync.Mutex
	rng   *rand.Rand
}

// NewRatioSampler creates a sampler that samples the given fraction of traces.
// Ratio must be between 0 and 1.
func NewRatioSampler(ratio float64) *RatioSampler {
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	return &RatioSampler{
		ratio: ratio,
		rng:   rand.New(rand.NewSource(rand.Int63())),
	}
}

// ShouldSample samples based on the configured ratio.
func (s *RatioSampler) ShouldSample(traceID internal.TraceID, name string, parentSampled bool) SamplingResult {
	s.mu.Lock()
	sample := s.rng.Float64() < s.ratio
	s.mu.Unlock()

	if sample {
		return SamplingResult{Decision: SamplingDecisionRecordAndSample}
	}
	return SamplingResult{Decision: SamplingDecisionDrop}
}

// ParentBasedSampler makes sampling decisions based on the parent span.
type ParentBasedSampler struct {
	root Sampler
}

// NewParentBasedSampler creates a sampler that follows the parent's sampling decision.
// If there is no parent, it uses the provided root sampler.
func NewParentBasedSampler(root Sampler) *ParentBasedSampler {
	return &ParentBasedSampler{root: root}
}

// ShouldSample follows the parent's decision or delegates to the root sampler.
func (s *ParentBasedSampler) ShouldSample(traceID internal.TraceID, name string, parentSampled bool) SamplingResult {
	if parentSampled {
		return SamplingResult{Decision: SamplingDecisionRecordAndSample}
	}
	if s.root != nil {
		return s.root.ShouldSample(traceID, name, parentSampled)
	}
	return SamplingResult{Decision: SamplingDecisionDrop}
}
