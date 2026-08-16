package bedrock

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kzs0/bedrock/trace"
	"github.com/kzs0/bedrock/trace/otlp"
)

func TestBedrockShutdownWaitsForBatchAndShutsExporterOnce(t *testing.T) {
	exporter := newControlledShutdownExporter()
	processor := otlp.NewBatchProcessor(exporter, otlp.BatchProcessorConfig{
		MaxQueueSize: 10,
		BatchSize:    1,
		BatchTimeout: time.Hour,
	})
	b := &Bedrock{
		batchProcessor: processor,
		rawExporter:    exporter,
		tracer: trace.NewTracer(trace.TracerConfig{
			ServiceName: "test",
			Exporter:    processor,
		}),
	}

	_, span := trace.NewTracer(trace.TracerConfig{ServiceName: "test"}).Start(context.Background(), "span")
	span.End()
	processor.EnqueueSpan(span)
	receiveRootSignal(t, exporter.started, "export start")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.Shutdown(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown with canceled context = %v, want context.Canceled", err)
	}
	assertRootNoSignal(t, exporter.shutdownCalled, "exporter shutdown before export completion")

	result := make(chan error, 1)
	go func() { result <- b.Shutdown(context.Background()) }()
	select {
	case err := <-result:
		t.Fatalf("Shutdown returned before export completion: %v", err)
	default:
	}

	exporter.release <- struct{}{}
	receiveRootSignal(t, exporter.finished, "export completion")
	if err := receiveRootShutdownResult(t, result); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := b.Shutdown(context.Background()); err != nil {
		t.Fatalf("repeated Shutdown: %v", err)
	}

	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	if exporter.premature {
		t.Error("exporter was shut down while an export was active")
	}
	if exporter.shutdownCalls != 1 {
		t.Errorf("exporter Shutdown calls = %d, want 1", exporter.shutdownCalls)
	}
	if len(exporter.spans) != 1 || exporter.spans[0] != span {
		t.Errorf("exported spans = %v, want exactly the queued span", exporter.spans)
	}
}

func TestBedrockShutdownReturnsBatchErrorAndStillClosesExporter(t *testing.T) {
	wantErr := errors.New("batch export failed")
	exporter := &rootFailingExporter{err: wantErr}
	processor := otlp.NewBatchProcessor(exporter, otlp.BatchProcessorConfig{
		MaxQueueSize: 10,
		BatchSize:    10,
		BatchTimeout: time.Hour,
	})
	b := &Bedrock{batchProcessor: processor, rawExporter: exporter}

	_, span := trace.NewTracer(trace.TracerConfig{ServiceName: "test"}).Start(context.Background(), "span")
	span.End()
	processor.EnqueueSpan(span)

	if err := b.Shutdown(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Shutdown error = %v, want %v", err, wantErr)
	}
	if err := b.Shutdown(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("repeated Shutdown error = %v, want %v", err, wantErr)
	}
	if got := exporter.shutdownCalls.Load(); got != 1 {
		t.Fatalf("exporter Shutdown calls = %d, want 1", got)
	}
}

type rootFailingExporter struct {
	err           error
	shutdownCalls atomic.Int32
}

func (e *rootFailingExporter) ExportSpans(context.Context, []*trace.Span) error {
	return e.err
}

func (e *rootFailingExporter) Shutdown(context.Context) error {
	e.shutdownCalls.Add(1)
	return nil
}

type controlledShutdownExporter struct {
	started        chan struct{}
	release        chan struct{}
	finished       chan struct{}
	shutdownCalled chan struct{}

	mu            sync.Mutex
	active        int
	shutdownCalls int
	premature     bool
	spans         []*trace.Span
}

func newControlledShutdownExporter() *controlledShutdownExporter {
	return &controlledShutdownExporter{
		started:        make(chan struct{}, 1),
		release:        make(chan struct{}, 1),
		finished:       make(chan struct{}, 1),
		shutdownCalled: make(chan struct{}, 1),
	}
}

func (e *controlledShutdownExporter) ExportSpans(_ context.Context, spans []*trace.Span) error {
	e.mu.Lock()
	e.active++
	e.mu.Unlock()
	e.started <- struct{}{}
	<-e.release
	e.mu.Lock()
	e.spans = append(e.spans, spans...)
	e.active--
	e.mu.Unlock()
	e.finished <- struct{}{}
	return nil
}

func (e *controlledShutdownExporter) Shutdown(_ context.Context) error {
	e.mu.Lock()
	if e.active != 0 {
		e.premature = true
	}
	e.shutdownCalls++
	e.mu.Unlock()
	e.shutdownCalled <- struct{}{}
	return nil
}

func receiveRootSignal(t *testing.T, ch <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func assertRootNoSignal(t *testing.T, ch <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal(description)
	default:
	}
}

func receiveRootShutdownResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for shutdown")
		return nil
	}
}
