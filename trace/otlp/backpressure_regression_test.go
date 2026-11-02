package otlp

import (
	"context"
	"testing"
	"time"

	"github.com/kzs0/bedrock/trace"
)

type pressureExporter struct {
	started chan struct{}
	release chan struct{}
}

func (e *pressureExporter) ExportSpans(ctx context.Context, _ []*trace.Span) error {
	e.started <- struct{}{}
	select {
	case <-e.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (e *pressureExporter) Shutdown(context.Context) error { return nil }
func TestBatchProcessorBoundsOutstandingExports(t *testing.T) {
	exp := &pressureExporter{started: make(chan struct{}, 100), release: make(chan struct{})}
	bp := NewBatchProcessor(exp, BatchProcessorConfig{MaxQueueSize: 2, BatchSize: 1, BatchTimeout: time.Hour})
	defer func() {
		close(exp.release)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := bp.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	bp.EnqueueSpan(newBatchTestSpan())
	select {
	case <-exp.started:
	case <-time.After(time.Second):
		t.Fatal("initial export did not start")
	}
	for i := 0; i < 50; i++ {
		bp.EnqueueSpan(newBatchTestSpan())
	}
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	active := 1
	for {
		select {
		case <-exp.started:
			active++
			if active > 2 {
				t.Fatalf("queue budget 2 allowed %d outstanding exports", active)
			}
		case <-timer.C:
			return
		}
	}
}
