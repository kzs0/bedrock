package e2e

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kzs0/bedrock/trace/otlp"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type faultTraceCollector struct {
	collector.UnimplementedTraceServiceServer
	requests chan *collector.ExportTraceServiceRequest
	fail     atomic.Bool
	block    bool
	canceled chan struct{}
}

func (s *faultTraceCollector) Export(ctx context.Context, request *collector.ExportTraceServiceRequest) (*collector.ExportTraceServiceResponse, error) {
	select {
	case s.requests <- request:
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if s.block {
		<-ctx.Done()
		close(s.canceled)
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if s.fail.Load() {
		return nil, status.Error(codes.Unavailable, "temporary collector outage")
	}
	return &collector.ExportTraceServiceResponse{}, nil
}

func serveFaultCollector(t *testing.T, address string, sink *faultTraceCollector) (*grpc.Server, string) {
	t.Helper()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	collector.RegisterTraceServiceServer(server, sink)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("gRPC serve: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("gRPC server did not stop")
		}
	})
	return server, listener.Addr().String()
}

func nextFaultExport(t *testing.T, sink *faultTraceCollector) *collector.ExportTraceServiceRequest {
	t.Helper()
	select {
	case request := <-sink.requests:
		return request
	case <-time.After(12 * time.Second):
		t.Fatal("gRPC export deadline exceeded")
		return nil
	}
}

func TestGRPCUnavailableRecoveryAndReconnect(t *testing.T) {
	sink := &faultTraceCollector{requests: make(chan *collector.ExportTraceServiceRequest, 16)}
	sink.fail.Store(true)
	server, address := serveFaultCollector(t, "127.0.0.1:0", sink)
	app := startApp(t, fixtureConfig{TraceEndpoint: address, TraceProtocol: "grpc", TraceInsecure: true})
	n := otlp.DefaultBatchConfig().BatchSize
	seen := make(map[string]bool)
	capturedSpans := 0
	checkBatch := func(request *collector.ExportTraceServiceRequest) {
		spans := validateGRPCRequest(t, request)
		if len(spans) != n {
			t.Fatalf("batch spans: %d, want %d", len(spans), n)
		}
		for _, span := range spans {
			id := string(span.TraceId) + string(span.SpanId)
			if seen[id] {
				t.Fatalf("duplicate gRPC span: %x/%x", span.TraceId, span.SpanId)
			}
			seen[id] = true
		}
		capturedSpans += len(spans)
	}
	emitOperations(t, app, n, "")
	checkBatch(nextFaultExport(t, sink))
	stats := awaitBehaviorStats(t, app, func(stats behaviorStats) bool { return stats.Trace.FailedExports == 1 })
	if stats.Trace.DroppedSpans != 0 {
		t.Fatalf("unexpected queue loss: %+v", stats.Trace)
	}
	server.Stop()
	emitOperations(t, app, n, "")
	awaitBehaviorStats(t, app, func(stats behaviorStats) bool { return stats.Trace.FailedExports == 2 })
	reconnected := &faultTraceCollector{requests: make(chan *collector.ExportTraceServiceRequest, 16)}
	_, _ = serveFaultCollector(t, address, reconnected)
	emitOperations(t, app, n, "&payload_bytes=256")
	recovered := nextFaultExport(t, reconnected)
	checkBatch(recovered)
	spans := validateGRPCRequest(t, recovered)
	if len(spans) != n || proto.Size(recovered) <= 64<<10 {
		t.Fatalf("large recovered batch: %d spans, %d bytes", len(spans), proto.Size(recovered))
	}
	for _, span := range spans {
		if len(behaviorStringAttr(span, "fixture.payload")) != 256 {
			t.Fatalf("large batch attribute lost: %v", span)
		}
	}
	emitOperations(t, app, n, "")
	checkBatch(nextFaultExport(t, reconnected))
	if err := app.StopError(t); err == nil {
		t.Fatal("shutdown omitted historical Unavailable failure")
	}
	final := app.FinalStats(t)
	if final.Trace.FailedExports != 2 || final.Trace.DroppedSpans != 0 {
		t.Fatalf("recovery changed historical losses: %+v", final.Trace)
	}
	if capturedSpans != 3*n || (final.Trace.FailedExports+2)*uint64(n) != uint64(4*n) {
		t.Fatalf("four emitted batches: captured %d spans, failed %d batches, successful 2", capturedSpans, final.Trace.FailedExports)
	}
	for _, captures := range []chan *collector.ExportTraceServiceRequest{sink.requests, reconnected.requests} {
		select {
		case extra := <-captures:
			t.Fatalf("unexpected extra gRPC export: %v", extra)
		default:
		}
	}
}

func TestGRPCStalledExportHonorsShutdownCancellation(t *testing.T) {
	sink := &faultTraceCollector{requests: make(chan *collector.ExportTraceServiceRequest, 16), block: true, canceled: make(chan struct{})}
	_, address := serveFaultCollector(t, "127.0.0.1:0", sink)
	app := startApp(t, fixtureConfig{TraceEndpoint: address, TraceProtocol: "grpc", TraceInsecure: true, ShutdownMillis: 500})
	n := otlp.DefaultBatchConfig().BatchSize
	emitOperations(t, app, n, "")
	if got := len(validateGRPCRequest(t, nextFaultExport(t, sink))); got != n {
		t.Fatalf("stalled batch spans: %d, want %d", got, n)
	}
	before := time.Now()
	if err := app.StopError(t); err == nil || time.Since(before) > 3*time.Second {
		t.Fatalf("bounded gRPC shutdown failed: %v, elapsed %s", err, time.Since(before))
	}
	assertCancellationError(t, app)
	select {
	case <-sink.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("gRPC server context was not canceled")
	}
	// Deadline snapshots may precede asynchronous failure accounting.
	if stats := app.FinalStats(t); stats.Trace.DroppedSpans != 0 || stats.Trace.FailedExports > 1 {
		t.Fatalf("unexpected cancellation losses: %+v", stats.Trace)
	}
}
