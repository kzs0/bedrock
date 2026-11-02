package otlp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kzs0/bedrock/trace"
)

// BatchProcessorConfig configures bounded span batching.
type BatchProcessorConfig struct {
	MaxQueueSize int
	BatchSize    int
	BatchTimeout time.Duration
}

// DefaultBatchConfig returns production batching defaults.
func DefaultBatchConfig() BatchProcessorConfig {
	return BatchProcessorConfig{2048, 512, 5 * time.Second}
}

// BatchStats reports cumulative export losses.
type BatchStats struct {
	DroppedSpans  uint64
	FailedExports uint64
}

// BatchProcessor exports through one bounded worker.
type BatchProcessor struct {
	cfg          BatchProcessorConfig
	exporter     trace.Exporter
	mu           sync.Mutex
	queue        []*trace.Span
	inFlight     int
	stopped      bool
	started      sync.Once
	shutdown     sync.Once
	wake         chan struct{}
	done         chan struct{}
	exportCtx    context.Context
	cancelExport context.CancelFunc
	err          error
	errorCount   int
	dropped      atomic.Uint64
	failed       atomic.Uint64
}

// NewBatchProcessor retains caller ownership of exporter.
func NewBatchProcessor(exporter trace.Exporter, cfg BatchProcessorConfig) *BatchProcessor {
	defaults := DefaultBatchConfig()
	if cfg.MaxQueueSize <= 0 {
		cfg.MaxQueueSize = defaults.MaxQueueSize
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaults.BatchSize
	}
	if cfg.BatchTimeout <= 0 {
		cfg.BatchTimeout = defaults.BatchTimeout
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &BatchProcessor{cfg: cfg, exporter: exporter, wake: make(chan struct{}, 1), done: make(chan struct{}), exportCtx: ctx, cancelExport: cancel}
}

// EnqueueSpan drops newest spans beyond budget.
func (bp *BatchProcessor) EnqueueSpan(span *trace.Span) {
	bp.mu.Lock()
	if bp.stopped || len(bp.queue)+bp.inFlight >= bp.cfg.MaxQueueSize {
		bp.dropped.Add(1)
		bp.mu.Unlock()
		return
	}
	bp.queue = append(bp.queue, span)
	bp.mu.Unlock()
	bp.start()
	bp.signal()
}
func (bp *BatchProcessor) start() { bp.started.Do(func() { go bp.run() }) }
func (bp *BatchProcessor) signal() {
	select {
	case bp.wake <- struct{}{}:
	default:
	}
}
func (bp *BatchProcessor) run() {
	defer close(bp.done)
	defer bp.cancelExport()
	timer := time.NewTicker(bp.cfg.BatchTimeout)
	defer timer.Stop()
	for {
		expired := false
		select {
		case <-bp.wake:
		case <-timer.C:
			expired = true
		}
		for {
			bp.mu.Lock()
			if len(bp.queue) == 0 {
				stopped := bp.stopped
				bp.mu.Unlock()
				if stopped {
					return
				}
				break
			}
			if !expired && !bp.stopped && len(bp.queue) < bp.cfg.BatchSize {
				bp.mu.Unlock()
				break
			}
			n := min(len(bp.queue), bp.cfg.BatchSize)
			spans := append([]*trace.Span(nil), bp.queue[:n]...)
			copy(bp.queue, bp.queue[n:])
			clear(bp.queue[len(bp.queue)-n:])
			bp.queue = bp.queue[:len(bp.queue)-n]
			bp.inFlight = n
			bp.mu.Unlock()
			err := bp.exporter.ExportSpans(bp.exportCtx, spans)
			bp.mu.Lock()
			bp.inFlight = 0
			if err != nil {
				bp.failed.Add(1)
				if bp.errorCount < 16 {
					bp.err = errors.Join(bp.err, err)
					bp.errorCount++
				}
			}
			bp.mu.Unlock()
		}
	}
}

// ExportSpans enqueues without blocking on network.
func (bp *BatchProcessor) ExportSpans(_ context.Context, spans []*trace.Span) error {
	for _, s := range spans {
		bp.EnqueueSpan(s)
	}
	return nil
}

// Stats returns cumulative drops and failures.
func (bp *BatchProcessor) Stats() BatchStats { return BatchStats{bp.dropped.Load(), bp.failed.Load()} }

// Shutdown drains accepted spans without closing exporter.
func (bp *BatchProcessor) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	bp.shutdown.Do(func() {
		stop := context.AfterFunc(ctx, bp.cancelExport)
		bp.mu.Lock()
		bp.stopped = true
		bp.mu.Unlock()
		bp.start()
		bp.signal()
		go func() { <-bp.done; stop() }()
	})
	select {
	case <-bp.done:
		return bp.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
