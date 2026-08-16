package otlp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kzs0/bedrock/trace"
)

type batchMockExporter struct {
	mu     sync.Mutex
	spans  []*trace.Span
	calls  atomic.Int32
	called chan struct{}
}

func (m *batchMockExporter) ExportSpans(_ context.Context, spans []*trace.Span) error {
	m.calls.Add(1)
	m.mu.Lock()
	m.spans = append(m.spans, spans...)
	m.mu.Unlock()
	if m.called != nil {
		m.called <- struct{}{}
	}
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

	_ = bp.Shutdown(context.Background())
}

func TestBatchProcessor_EnqueueSpan(t *testing.T) {
	exp := &batchMockExporter{called: make(chan struct{}, 1)}
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

	select {
	case <-exp.called:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for batch-size export")
	}

	_ = bp.Shutdown(context.Background())
}

func TestBatchProcessor_FlushOnTimeout(t *testing.T) {
	exp := &batchMockExporter{called: make(chan struct{}, 1)}
	bp := NewBatchProcessor(exp, BatchProcessorConfig{
		MaxQueueSize: 100,
		BatchSize:    100, // Large batch size so it won't trigger by count
		BatchTimeout: 50 * time.Millisecond,
	})

	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.Start(context.Background(), "test")
	span.End()
	bp.EnqueueSpan(span)

	select {
	case <-exp.called:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for timer export")
	}

	_ = bp.Shutdown(context.Background())
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

	_ = bp.Shutdown(context.Background())
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

	_ = bp.Shutdown(context.Background())

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
	_ = bp.Shutdown(context.Background())
}

func TestBatchProcessor_ShutdownWaitsForFullBatchExport(t *testing.T) {
	exp := newControlledBatchExporter()
	bp := NewBatchProcessor(exp, BatchProcessorConfig{
		MaxQueueSize: 10,
		BatchSize:    1,
		BatchTimeout: time.Hour,
	})
	span := newBatchTestSpan()
	bp.EnqueueSpan(span)

	started := receiveStartedBatch(t, exp)
	if len(started) != 1 || started[0] != span {
		t.Fatalf("started batch = %v, want the enqueued span", started)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := bp.Shutdown(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown with canceled context = %v, want context.Canceled", err)
	}
	assertExporterNotShutdown(t, exp)

	result := make(chan error, 1)
	go func() { result <- bp.Shutdown(context.Background()) }()
	select {
	case err := <-result:
		t.Fatalf("Shutdown returned before in-flight export completed: %v", err)
	default:
	}

	exp.release <- struct{}{}
	receiveExportFinished(t, exp)
	if err := receiveBatchShutdownResult(t, result); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	exp.assertLifecycle(t, 0, span)
}

func TestBatchProcessor_ShutdownWaitsForTimerExport(t *testing.T) {
	exp := newControlledBatchExporter()
	bp := NewBatchProcessor(exp, BatchProcessorConfig{
		MaxQueueSize: 10,
		BatchSize:    10,
		BatchTimeout: 10 * time.Millisecond,
	})
	span := newBatchTestSpan()
	bp.EnqueueSpan(span)

	started := receiveStartedBatch(t, exp)
	if len(started) != 1 || started[0] != span {
		t.Fatalf("started timer batch = %v, want the enqueued span", started)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := bp.Shutdown(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown with canceled context = %v, want context.Canceled", err)
	}
	assertExporterNotShutdown(t, exp)

	result := make(chan error, 1)
	go func() { result <- bp.Shutdown(context.Background()) }()
	select {
	case err := <-result:
		t.Fatalf("Shutdown returned before timer export completed: %v", err)
	default:
	}

	exp.release <- struct{}{}
	receiveExportFinished(t, exp)
	if err := receiveBatchShutdownResult(t, result); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	exp.assertLifecycle(t, 0, span)
}

func TestBatchProcessor_ShutdownFlushesQueuedBatchWithoutClosingExporter(t *testing.T) {
	exp := newControlledBatchExporter()
	bp := NewBatchProcessor(exp, BatchProcessorConfig{
		MaxQueueSize: 10,
		BatchSize:    10,
		BatchTimeout: time.Hour,
	})
	span := newBatchTestSpan()
	bp.EnqueueSpan(span)

	result := make(chan error, 1)
	go func() { result <- bp.Shutdown(context.Background()) }()
	started := receiveStartedBatch(t, exp)
	if len(started) != 1 || started[0] != span {
		t.Fatalf("shutdown batch = %v, want the queued span", started)
	}
	select {
	case err := <-result:
		t.Fatalf("Shutdown returned before queued export completed: %v", err)
	default:
	}
	assertExporterNotShutdown(t, exp)

	exp.release <- struct{}{}
	receiveExportFinished(t, exp)
	if err := receiveBatchShutdownResult(t, result); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	exp.assertLifecycle(t, 0, span)
}

func TestBatchProcessor_ShutdownReturnsFinalExportError(t *testing.T) {
	wantErr := errors.New("final export failed")
	exp := &failingBatchExporter{err: wantErr}
	bp := NewBatchProcessor(exp, BatchProcessorConfig{
		MaxQueueSize: 10,
		BatchSize:    10,
		BatchTimeout: time.Hour,
	})
	bp.EnqueueSpan(newBatchTestSpan())

	if err := bp.Shutdown(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Shutdown error = %v, want %v", err, wantErr)
	}
	if err := bp.Shutdown(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("repeated Shutdown error = %v, want %v", err, wantErr)
	}
	if got := exp.shutdownCalls.Load(); got != 0 {
		t.Fatalf("exporter Shutdown calls = %d, want 0", got)
	}
}

func TestBatchProcessor_ShutdownReturnsAsynchronousExportError(t *testing.T) {
	wantErr := errors.New("asynchronous export failed")
	exp := &failingBatchExporter{err: wantErr, called: make(chan struct{}, 1)}
	bp := NewBatchProcessor(exp, BatchProcessorConfig{
		MaxQueueSize: 10,
		BatchSize:    1,
		BatchTimeout: time.Hour,
	})
	bp.EnqueueSpan(newBatchTestSpan())
	select {
	case <-exp.called:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for asynchronous export")
	}

	if err := bp.Shutdown(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Shutdown error = %v, want %v", err, wantErr)
	}
}

func TestBatchProcessor_ShutdownCancelsInFlightExport(t *testing.T) {
	exp := &cancelAwareBatchExporter{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	bp := NewBatchProcessor(exp, BatchProcessorConfig{
		MaxQueueSize: 10,
		BatchSize:    1,
		BatchTimeout: time.Hour,
	})
	bp.EnqueueSpan(newBatchTestSpan())
	select {
	case <-exp.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for export start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := bp.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown error = %v, want context.Canceled", err)
	}
	select {
	case <-exp.canceled:
	case <-time.After(time.Second):
		t.Fatal("in-flight export did not observe shutdown cancellation")
	}
	if err := bp.Shutdown(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("completed Shutdown error = %v, want canceled export error", err)
	}
	if got := exp.shutdownCalls.Load(); got != 0 {
		t.Fatalf("exporter Shutdown calls = %d, want 0", got)
	}
}

type failingBatchExporter struct {
	err           error
	called        chan struct{}
	shutdownCalls atomic.Int32
}

func (e *failingBatchExporter) ExportSpans(context.Context, []*trace.Span) error {
	if e.called != nil {
		e.called <- struct{}{}
	}
	return e.err
}

func (e *failingBatchExporter) Shutdown(context.Context) error {
	e.shutdownCalls.Add(1)
	return nil
}

type cancelAwareBatchExporter struct {
	started       chan struct{}
	canceled      chan struct{}
	shutdownCalls atomic.Int32
}

func (e *cancelAwareBatchExporter) ExportSpans(ctx context.Context, _ []*trace.Span) error {
	close(e.started)
	<-ctx.Done()
	close(e.canceled)
	return ctx.Err()
}

func (e *cancelAwareBatchExporter) Shutdown(context.Context) error {
	e.shutdownCalls.Add(1)
	return nil
}

type controlledBatchExporter struct {
	started        chan []*trace.Span
	release        chan struct{}
	finished       chan struct{}
	shutdownCalled chan struct{}

	mu            sync.Mutex
	spans         []*trace.Span
	active        int
	shutdownCalls int
	premature     bool
}

func newControlledBatchExporter() *controlledBatchExporter {
	return &controlledBatchExporter{
		started:        make(chan []*trace.Span, 4),
		release:        make(chan struct{}, 4),
		finished:       make(chan struct{}, 4),
		shutdownCalled: make(chan struct{}, 4),
	}
}

func (e *controlledBatchExporter) ExportSpans(_ context.Context, spans []*trace.Span) error {
	e.mu.Lock()
	e.active++
	e.mu.Unlock()

	e.started <- spans
	<-e.release

	e.mu.Lock()
	e.spans = append(e.spans, spans...)
	e.active--
	e.mu.Unlock()
	e.finished <- struct{}{}
	return nil
}

func (e *controlledBatchExporter) Shutdown(_ context.Context) error {
	e.mu.Lock()
	if e.active != 0 {
		e.premature = true
	}
	e.shutdownCalls++
	e.mu.Unlock()
	e.shutdownCalled <- struct{}{}
	return nil
}

func (e *controlledBatchExporter) assertLifecycle(t *testing.T, wantCalls int, wantSpan *trace.Span) {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.premature {
		t.Error("exporter was shut down while an export was active")
	}
	if e.shutdownCalls != wantCalls {
		t.Errorf("exporter Shutdown calls = %d, want %d", e.shutdownCalls, wantCalls)
	}
	if len(e.spans) != 1 || e.spans[0] != wantSpan {
		t.Errorf("exported spans = %v, want exactly the queued span", e.spans)
	}
}

func newBatchTestSpan() *trace.Span {
	tracer := trace.NewTracer(trace.TracerConfig{ServiceName: "test"})
	_, span := tracer.Start(context.Background(), "test")
	span.End()
	return span
}

func receiveStartedBatch(t *testing.T, exp *controlledBatchExporter) []*trace.Span {
	t.Helper()
	select {
	case spans := <-exp.started:
		return spans
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for export to start")
		return nil
	}
}

func receiveExportFinished(t *testing.T, exp *controlledBatchExporter) {
	t.Helper()
	select {
	case <-exp.finished:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for export to finish")
	}
}

func assertExporterNotShutdown(t *testing.T, exp *controlledBatchExporter) {
	t.Helper()
	select {
	case <-exp.shutdownCalled:
		t.Fatal("exporter was shut down before its export finished")
	default:
	}
}

func receiveBatchShutdownResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for batch shutdown")
		return nil
	}
}
