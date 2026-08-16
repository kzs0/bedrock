package otlp

import (
	"context"
	"errors"
	"sort"
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
	exporter trace.Exporter

	mu       sync.Mutex
	queue    []*trace.Span
	timer    *time.Timer
	stopped  bool
	exports  sync.WaitGroup
	shutdown sync.Once
	done     chan struct{}
	err      error

	exportCtx    context.Context
	cancelExport context.CancelFunc
	errMu        sync.Mutex
	exportErrors map[uint64]error
	nextExportID uint64
}

// NewBatchProcessor creates a new batch processor. The caller retains ownership
// of exporter and must shut it down after the processor has drained.
func NewBatchProcessor(exporter trace.Exporter, cfg BatchProcessorConfig) *BatchProcessor {
	if cfg.MaxQueueSize <= 0 {
		cfg.MaxQueueSize = 2048
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 512
	}
	if cfg.BatchTimeout <= 0 {
		cfg.BatchTimeout = 5 * time.Second
	}

	exportCtx, cancelExport := context.WithCancel(context.Background())
	bp := &BatchProcessor{
		cfg:          cfg,
		exporter:     exporter,
		queue:        make([]*trace.Span, 0, cfg.BatchSize),
		exportCtx:    exportCtx,
		cancelExport: cancelExport,
		exportErrors: make(map[uint64]error),
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
	if bp.stopped {
		bp.mu.Unlock()
		return
	}
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

	// Add while holding bp.mu. Shutdown takes the same lock before waiting, so
	// Wait can never race with a later Add.
	exportID := bp.nextExportID
	bp.nextExportID++
	bp.exports.Add(1)
	go func() {
		defer bp.exports.Done()
		if err := bp.exporter.ExportSpans(bp.exportCtx, spans); err != nil {
			bp.errMu.Lock()
			bp.exportErrors[exportID] = err
			bp.errMu.Unlock()
		}
	}()
}

// ExportSpans implements the trace.Exporter interface by enqueuing spans for
// batched export. This avoids spawning a goroutine per span.
func (bp *BatchProcessor) ExportSpans(_ context.Context, spans []*trace.Span) error {
	for _, s := range spans {
		bp.EnqueueSpan(s)
	}
	return nil
}

// Shutdown stops the processor and exports remaining spans.
func (bp *BatchProcessor) Shutdown(ctx context.Context) error {
	bp.shutdown.Do(func() {
		bp.done = make(chan struct{})
		stopContextCancellation := context.AfterFunc(ctx, bp.cancelExport)

		bp.mu.Lock()
		bp.stopped = true
		if bp.timer != nil {
			bp.timer.Stop()
			bp.timer = nil
		}
		// Track the final queued batch exactly like timer- and size-triggered
		// exports so one wait covers every export started by this processor.
		bp.exportLocked()
		exportCount := bp.nextExportID
		bp.mu.Unlock()

		go func() {
			bp.exports.Wait()
			stopContextCancellation()
			bp.cancelExport()
			bp.err = bp.joinExportErrors(exportCount)
			close(bp.done)
		}()
	})

	select {
	case <-bp.done:
		return bp.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (bp *BatchProcessor) joinExportErrors(exportCount uint64) error {
	bp.errMu.Lock()
	defer bp.errMu.Unlock()
	ids := make([]uint64, 0, len(bp.exportErrors))
	for id := range bp.exportErrors {
		if id < exportCount {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	errs := make([]error, 0, len(bp.exportErrors))
	for _, id := range ids {
		errs = append(errs, bp.exportErrors[id])
	}
	return errors.Join(errs...)
}
