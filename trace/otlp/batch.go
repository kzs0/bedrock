package otlp

import (
	"context"
	"sync"
	"time"

	"github.com/kzs0/bedrock/trace"
)

// BatchProcessorConfig configures the batch processor.
type BatchProcessorConfig struct {
	// MaxQueueSize is the maximum number of spans to queue.
	MaxQueueSize int
	// BatchSize is the maximum number of spans per export.
	BatchSize int
	// BatchTimeout is the maximum time to wait before exporting.
	BatchTimeout time.Duration
}

// DefaultBatchConfig returns default batch processor configuration.
func DefaultBatchConfig() BatchProcessorConfig {
	return BatchProcessorConfig{
		MaxQueueSize: 2048,
		BatchSize:    512,
		BatchTimeout: 5 * time.Second,
	}
}

// BatchProcessor batches spans before sending to an exporter.
type BatchProcessor struct {
	cfg      BatchProcessorConfig
	exporter *Exporter

	mu      sync.Mutex
	queue   []*trace.Span
	timer   *time.Timer
	stopped bool
	done    chan struct{}
}

// NewBatchProcessor creates a new batch processor.
func NewBatchProcessor(exporter *Exporter, cfg BatchProcessorConfig) *BatchProcessor {
	if cfg.MaxQueueSize <= 0 {
		cfg.MaxQueueSize = 2048
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 512
	}
	if cfg.BatchTimeout <= 0 {
		cfg.BatchTimeout = 5 * time.Second
	}

	bp := &BatchProcessor{
		cfg:      cfg,
		exporter: exporter,
		queue:    make([]*trace.Span, 0, cfg.BatchSize),
		done:     make(chan struct{}),
	}

	return bp
}

// EnqueueSpan adds a span to the queue for batched export.
func (bp *BatchProcessor) EnqueueSpan(span *trace.Span) {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	if bp.stopped {
		return
	}

	// Drop oldest spans if queue is full
	if len(bp.queue) >= bp.cfg.MaxQueueSize {
		bp.queue = bp.queue[1:]
	}

	bp.queue = append(bp.queue, span)

	// Start timer if this is the first span
	if len(bp.queue) == 1 {
		bp.timer = time.AfterFunc(bp.cfg.BatchTimeout, bp.flush)
	}

	// Export if batch is full
	if len(bp.queue) >= bp.cfg.BatchSize {
		bp.exportLocked()
	}
}

// flush exports the current batch.
func (bp *BatchProcessor) flush() {
	bp.mu.Lock()
	bp.exportLocked()
	bp.mu.Unlock()
}

// exportLocked exports spans while holding the lock.
func (bp *BatchProcessor) exportLocked() {
	if len(bp.queue) == 0 {
		return
	}

	if bp.timer != nil {
		bp.timer.Stop()
		bp.timer = nil
	}

	spans := bp.queue
	bp.queue = make([]*trace.Span, 0, bp.cfg.BatchSize)

	// Export in background
	go bp.exporter.ExportSpans(context.Background(), spans)
}

// Shutdown stops the processor and exports remaining spans.
func (bp *BatchProcessor) Shutdown(ctx context.Context) error {
	bp.mu.Lock()
	if bp.stopped {
		bp.mu.Unlock()
		return nil
	}
	bp.stopped = true

	if bp.timer != nil {
		bp.timer.Stop()
	}

	// Export remaining spans
	if len(bp.queue) > 0 {
		spans := bp.queue
		bp.queue = nil
		bp.mu.Unlock()
		return bp.exporter.ExportSpans(ctx, spans)
	}

	bp.mu.Unlock()
	return nil
}
