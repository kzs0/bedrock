package otlp

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kzs0/bedrock/trace"
)

type batchMockExporter struct {
	mu    sync.Mutex
	spans []*trace.Span
	calls atomic.Int32
}

func (m *batchMockExporter) ExportSpans(_ context.Context, spans []*trace.Span) error {
	m.calls.Add(1)
	m.mu.Lock()
	m.spans = append(m.spans, spans...)
	m.mu.Unlock()
	return nil
}

func (m *batchMockExporter) Shutdown(_ context.Context) error { return nil }

func TestDefaultBatchConfig(t *testing.T) {
	cfg := DefaultBatchConfig()
	if cfg.MaxQueueSize != 2048 {
		t.Errorf("expected MaxQueueSize 2048, got %d", cfg.MaxQueueSize)
	}
	if cfg.BatchSize != 512 {
		t.Errorf("expected BatchSize 512, got %d", cfg.BatchSize)
	}
	if cfg.BatchTimeout != 5*time.Second {
		t.Errorf("expected BatchTimeout 5s, got %v", cfg.BatchTimeout)
	}
}

func TestNewBatchProcessor_Defaults(t *testing.T) {
	exp := &batchMockExporter{}
	bp := NewBatchProcessor(exp, BatchProcessorConfig{})

	if bp.cfg.MaxQueueSize != 2048 {
		t.Errorf("expected default MaxQueueSize 2048, got %d", bp.cfg.MaxQueueSize)
	}
	if bp.cfg.BatchSize != 512 {
		t.Errorf("expected default BatchSize 512, got %d", bp.cfg.BatchSize)
	}
	if bp.cfg.BatchTimeout != 5*time.Second {
		t.Errorf("expected default BatchTimeout 5s, got %v", bp.cfg.BatchTimeout)
	}

	bp.Shutdown(context.Background())
}

func TestBatchProcessor_EnqueueSpan(t *testing.T) {
	exp := &batchMockExporter{}
	bp := NewBatchProcessor(exp, BatchProcessorConfig{
		MaxQueueSize: 100,
		BatchSize:    5,
		BatchTimeout: time.Second,
	})

	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})

	// Enqueue 5 spans (should trigger batch export)
	for i := 0; i < 5; i++ {
		_, span := tracer.Start(context.Background(), "test")
		span.End()
		bp.EnqueueSpan(span)
	}

	// Wait for async export
	time.Sleep(100 * time.Millisecond)

	if exp.calls.Load() == 0 {
		t.Error("expected batch export after reaching BatchSize")
	}

	bp.Shutdown(context.Background())
}

func TestBatchProcessor_FlushOnTimeout(t *testing.T) {
	exp := &batchMockExporter{}
	bp := NewBatchProcessor(exp, BatchProcessorConfig{
		MaxQueueSize: 100,
		BatchSize:    100, // Large batch size so it won't trigger by count
		BatchTimeout: 50 * time.Millisecond,
	})

	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.Start(context.Background(), "test")
	span.End()
	bp.EnqueueSpan(span)

	// Wait for timeout flush
	time.Sleep(200 * time.Millisecond)

	if exp.calls.Load() == 0 {
		t.Error("expected flush after BatchTimeout")
	}

	bp.Shutdown(context.Background())
}

func TestBatchProcessor_ExportSpans(t *testing.T) {
	exp := &batchMockExporter{}
	bp := NewBatchProcessor(exp, BatchProcessorConfig{
		MaxQueueSize: 100,
		BatchSize:    10,
		BatchTimeout: time.Second,
	})

	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	var spans []*trace.Span
	for i := 0; i < 3; i++ {
		_, span := tracer.Start(context.Background(), "test")
		span.End()
		spans = append(spans, span)
	}

	err := bp.ExportSpans(context.Background(), spans)
	if err != nil {
		t.Fatalf("ExportSpans error: %v", err)
	}

	bp.Shutdown(context.Background())
}

func TestBatchProcessor_Shutdown_FlushesRemaining(t *testing.T) {
	exp := &batchMockExporter{}
	bp := NewBatchProcessor(exp, BatchProcessorConfig{
		MaxQueueSize: 100,
		BatchSize:    100, // Large batch so it won't flush by count
		BatchTimeout: 10 * time.Second,
	})

	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.Start(context.Background(), "test")
	span.End()
	bp.EnqueueSpan(span)

	// Shutdown should flush remaining
	err := bp.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("Shutdown error: %v", err)
	}

	if exp.calls.Load() == 0 {
		t.Error("expected remaining spans to be flushed on shutdown")
	}
}

func TestBatchProcessor_Shutdown_Idempotent(t *testing.T) {
	exp := &batchMockExporter{}
	bp := NewBatchProcessor(exp, BatchProcessorConfig{
		MaxQueueSize: 100,
		BatchSize:    100,
		BatchTimeout: 10 * time.Second,
	})

	if err := bp.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown error: %v", err)
	}
	if err := bp.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown error: %v", err)
	}
}

func TestBatchProcessor_EnqueueAfterShutdown(t *testing.T) {
	exp := &batchMockExporter{}
	bp := NewBatchProcessor(exp, BatchProcessorConfig{
		MaxQueueSize: 100,
		BatchSize:    1,
		BatchTimeout: time.Second,
	})

	bp.Shutdown(context.Background())

	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.Start(context.Background(), "test")
	span.End()

	// Should not panic
	bp.EnqueueSpan(span)
}

func TestBatchProcessor_QueueOverflow(t *testing.T) {
	exp := &batchMockExporter{}
	bp := NewBatchProcessor(exp, BatchProcessorConfig{
		MaxQueueSize: 3,
		BatchSize:    100, // Large so it won't auto-flush
		BatchTimeout: 10 * time.Second,
	})

	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	// Enqueue more than MaxQueueSize
	for i := 0; i < 5; i++ {
		_, span := tracer.Start(context.Background(), "test")
		span.End()
		bp.EnqueueSpan(span)
	}

	// Should not panic, oldest should be dropped
	bp.Shutdown(context.Background())
}
