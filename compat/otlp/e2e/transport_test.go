package e2e

import (
	"bytes"
	"context"
	"encoding/hex"
	"net"
	"net/http"
	"testing"
	"time"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type grpcCapture struct {
	request  *collector.ExportTraceServiceRequest
	metadata metadata.MD
}

type traceCollector struct {
	collector.UnimplementedTraceServiceServer
	requests chan grpcCapture
}

func (s *traceCollector) Export(ctx context.Context, request *collector.ExportTraceServiceRequest) (*collector.ExportTraceServiceResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	select {
	case s.requests <- grpcCapture{request, md.Copy()}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &collector.ExportTraceServiceResponse{}, nil
}

func TestGRPCTracePropagationAndShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	sink := &traceCollector{requests: make(chan grpcCapture, 16)}
	collector.RegisterTraceServiceServer(server, sink)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("gRPC server: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("gRPC server shutdown deadline exceeded")
		}
	})
	app := startApp(t, fixtureConfig{TraceEndpoint: listener.Addr().String(), TraceProtocol: "grpc", TraceInsecure: true})
	req, err := http.NewRequest(http.MethodGet, app.URL+"/ok/42", nil)
	if err != nil {
		t.Fatal(err)
	}
	const traceID = "0123456789abcdef0123456789abcdef"
	const parentID = "0123456789abcdef"
	req.Header.Set("traceparent", "00-"+traceID+"-"+parentID+"-01")
	if status := doBehaviorRequest(t, req).StatusCode; status != 204 {
		t.Fatalf("response: %d", status)
	}
	app.Stop(t)
	var capture grpcCapture
	select {
	case capture = <-sink.requests:
	case <-time.After(5 * time.Second):
		t.Fatalf("official gRPC collector received no export\n%s", app.Logs())
	}
	spans := validateGRPCRequest(t, capture.request)
	if len(spans) != 1 {
		t.Fatalf("span count %d, want 1", len(spans))
	}
	span := spans[0]
	if hex.EncodeToString(span.TraceId) != traceID || hex.EncodeToString(span.ParentSpanId) != parentID {
		t.Fatalf("incoming parent lost: %v", span)
	}
	if span.Kind != tracepb.Span_SPAN_KIND_SERVER || behaviorStringAttr(span, "http.path") != "/ok/{id}" || behaviorIntAttr(span, "http.status_code") != 204 {
		t.Fatalf("request semantics lost: %v", span)
	}
}

func validateGRPCRequest(t *testing.T, request *collector.ExportTraceServiceRequest) []*tracepb.Span {
	t.Helper()
	var spans []*tracepb.Span
	serviceFound := false
	for _, resource := range request.ResourceSpans {
		for _, attribute := range resource.GetResource().GetAttributes() {
			if attribute.Key == "service.name" && attribute.Value.GetStringValue() == "bedrock-e2e" {
				serviceFound = true
			}
		}
		for _, scope := range resource.ScopeSpans {
			for _, span := range scope.Spans {
				if len(span.TraceId) != 16 || len(span.SpanId) != 8 || bytes.Equal(span.TraceId, make([]byte, 16)) || bytes.Equal(span.SpanId, make([]byte, 8)) {
					t.Fatalf("invalid span identifiers: %v", span)
				}
				if span.StartTimeUnixNano == 0 || span.EndTimeUnixNano < span.StartTimeUnixNano {
					t.Fatalf("invalid span interval: %v", span)
				}
				spans = append(spans, span)
			}
		}
	}
	if !serviceFound {
		t.Fatal("missing service.name resource")
	}
	return spans
}
