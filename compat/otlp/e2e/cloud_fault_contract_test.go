package e2e

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type cloudFaultCollector struct {
	collector.UnimplementedTraceServiceServer
	requests chan *collector.ExportTraceServiceRequest
	reject   uint64
	attempts atomic.Uint64
	badAuth  atomic.Bool
}

func (s *cloudFaultCollector) Export(ctx context.Context, request *collector.ExportTraceServiceRequest) (*collector.ExportTraceServiceResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if values := md.Get("x-bedrock-key"); len(values) != 1 || values[0] != "cloud-fault-key" {
		s.badAuth.Store(true)
		return nil, status.Error(codes.Unauthenticated, "wrong key")
	}
	s.requests <- request
	if s.attempts.Add(1) <= s.reject {
		return nil, status.Error(codes.Unavailable, "collector outage")
	}
	return &collector.ExportTraceServiceResponse{}, nil
}

func nextCloudFault(t *testing.T, requests <-chan *collector.ExportTraceServiceRequest) *collector.ExportTraceServiceRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(12 * time.Second):
		t.Fatal("cloud trace attempt missing")
		return nil
	}
}

func TestPrivacyContractCloudRetryAndFanout(t *testing.T) {
	for _, tc := range []struct {
		name        string
		reject      uint64
		localStatus int
		batches     int
		attempts    int
		failures    uint64
	}{{"retry_recovers", 1, 200, 1, 2, 0}, {"exhaustion_then_recovery", 4, 200, 2, 5, 1}, {"local_failure_preserves_cloud", 0, 503, 1, 1, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &cloudFaultCollector{requests: make(chan *collector.ExportTraceServiceRequest, 16), reject: tc.reject}
			grpcServer := grpc.NewServer()
			collector.RegisterTraceServiceServer(grpcServer, sink)
			metrics := make(chan exportRequest, 16)
			server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
					grpcServer.ServeHTTP(w, r)
					return
				}
				body, _ := io.ReadAll(r.Body)
				metrics <- exportRequest{Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
				w.WriteHeader(http.StatusOK)
			}), &http2.Server{}))
			t.Cleanup(func() { grpcServer.Stop(); server.Close() })
			local := newReceiver(t)
			local.SetHandler(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.localStatus) })
			app := startApp(t, fixtureConfig{TraceEndpoint: local.URL + "/v1/traces", CloudEndpoint: server.URL, CloudAPIKey: "cloud-fault-key", CloudProfileTypes: []string{}, CloudPushIntervalMillis: 60000})
			emitOperations(t, app, 512, "")
			first := nextCloudFault(t, sink.requests)
			cloudBatches := []*collector.ExportTraceServiceRequest{first}
			if len(validateGRPCRequest(t, first)) != 512 {
				t.Fatal("first cloud batch lost spans")
			}
			for range min(tc.attempts, 4) - 1 {
				if next := nextCloudFault(t, sink.requests); !proto.Equal(first, next) {
					t.Fatal("cloud retry changed payload identity")
				}
			}
			if tc.batches == 2 {
				awaitBehaviorStats(t, app, func(stats behaviorStats) bool { return stats.Trace.FailedExports == 1 })
				emitOperations(t, app, 512, "")
				recovered := nextCloudFault(t, sink.requests)
				cloudBatches = append(cloudBatches, recovered)
				if len(validateGRPCRequest(t, recovered)) != 512 || proto.Equal(first, recovered) {
					t.Fatal("recovery did not export a fresh full batch")
				}
			}
			app.Stop(t)
			if stats := app.FinalStats(t); stats.Trace.FailedExports != tc.failures || stats.Trace.DroppedSpans != 0 {
				t.Fatalf("fanout counters: %+v", stats)
			}
			if sink.badAuth.Load() || sink.attempts.Load() != uint64(tc.attempts) {
				t.Fatalf("cloud attempts=%d badAuth=%t", sink.attempts.Load(), sink.badAuth.Load())
			}
			for batch := range tc.batches {
				localSpans := behaviorSpans(t, nextBehaviorExport(t, local.Requests))
				if len(localSpans) != 512 {
					t.Fatal("local fanout lost batch")
				}
				cloudSpans := validateGRPCRequest(t, cloudBatches[batch])
				for index, span := range localSpans {
					if string(span.TraceId) != string(cloudSpans[index].TraceId) || string(span.SpanId) != string(cloudSpans[index].SpanId) || span.Name != cloudSpans[index].Name {
						t.Fatal("local/cloud fanout changed span identity")
					}
				}
			}
			select {
			case extra := <-local.Requests:
				t.Fatalf("local fanout retried cloud failure: %v", extra)
			default:
			}
			select {
			case extra := <-sink.requests:
				t.Fatalf("extra cloud trace attempt: %v", extra)
			default:
			}
			request := nextBehaviorExport(t, metrics)
			if request.Path != "/v1/metrics" || request.Header.Get("Authorization") != "Bearer cloud-fault-key" || !strings.Contains(string(request.Body), "fixture_operation_count") {
				t.Fatalf("final authenticated metrics push: %s %s", request.Path, request.Body)
			}
			if tc.reject > 0 && !strings.Contains(app.Logs(), "cloud trace export failed, retrying") {
				t.Fatal("cloud retry failure was invisible")
			}
		})
	}
}

func TestPrivacyContractCloudStalledMetricsShutdown(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	var attempts atomic.Uint64
	var badAuth atomic.Bool
	requests := make(chan exportRequest, 8)
	grpcServer := grpc.NewServer()
	sink := &cloudFaultCollector{requests: make(chan *collector.ExportTraceServiceRequest, 8)}
	collector.RegisterTraceServiceServer(grpcServer, sink)
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer cloud-fault-key" {
			badAuth.Store(true)
		}
		requests <- exportRequest{Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
		if attempts.Add(1) == 1 {
			close(entered)
			<-r.Context().Done()
			close(canceled)
			return
		}
		w.WriteHeader(http.StatusOK)
	}), &http2.Server{}))
	defer func() { grpcServer.Stop(); server.Close() }()
	app := startApp(t, fixtureConfig{CloudEndpoint: server.URL, CloudAPIKey: "cloud-fault-key", CloudProfileTypes: []string{}, CloudPushIntervalMillis: 5000, ShutdownMillis: 1000})
	awaitResilienceSignal(t, entered, 8*time.Second, "periodic metrics push")
	resilienceResponsiveGET(t, app.URL+"/ok/during-cloud-stall")
	before := time.Now()
	app.Stop(t)
	if time.Since(before) > 3*time.Second {
		t.Fatal("cloud metrics shutdown exceeded bound")
	}
	awaitResilienceSignal(t, canceled, time.Second, "cloud metrics cancellation")
	if attempts.Load() != 2 || badAuth.Load() {
		t.Fatalf("metrics attempts=%d badAuth=%t", attempts.Load(), badAuth.Load())
	}
	first, final := nextBehaviorExport(t, requests), nextBehaviorExport(t, requests)
	if first.Path != "/v1/metrics" || final.Path != "/v1/metrics" {
		t.Fatalf("cloud push paths %q %q", first.Path, final.Path)
	}
	if value, count := behaviorMetric(string(final.Body), "http_request_count", "/ok/{id}"); value != 1 || count != 1 {
		t.Fatalf("final push missed concurrent request: count=%d value=%v", count, value)
	}
}

func TestPrivacyContractCloudMetricsRejectAndRecover(t *testing.T) {
	requests := make(chan exportRequest, 8)
	var attempts atomic.Uint64
	grpcServer := grpc.NewServer()
	sink := &cloudFaultCollector{requests: make(chan *collector.ExportTraceServiceRequest, 8)}
	collector.RegisterTraceServiceServer(grpcServer, sink)
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		requests <- exportRequest{Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}), &http2.Server{}))
	defer func() { grpcServer.Stop(); server.Close() }()
	app := startApp(t, fixtureConfig{CloudEndpoint: server.URL, CloudAPIKey: "cloud-fault-key", CloudProfileTypes: []string{}, CloudPushIntervalMillis: 500})
	first := nextBehaviorExport(t, requests)
	profileGET(t, app.URL+"/ok/after-metrics-rejection", http.StatusNoContent)
	second := nextBehaviorExport(t, requests)
	app.Stop(t)
	final := nextBehaviorExport(t, requests)
	for _, request := range []exportRequest{first, second, final} {
		if request.Path != "/v1/metrics" || request.Header.Get("Authorization") != "Bearer cloud-fault-key" {
			t.Fatalf("metrics rejection/recovery envelope: %s %v", request.Path, request.Header)
		}
	}
	for _, request := range []exportRequest{second, final} {
		if value, count := behaviorMetric(string(request.Body), "http_request_count", "/ok/{id}"); value != 1 || count != 1 {
			t.Fatalf("metrics recovery lost request: %d %v", count, value)
		}
	}
	if !strings.Contains(app.Logs(), "metrics push failed") || sink.badAuth.Load() {
		t.Fatalf("metrics rejection visibility or trace auth: %s", app.Logs())
	}
	if len(validateGRPCRequest(t, nextCloudFault(t, sink.requests))) != 1 {
		t.Fatal("cloud trace lost during metrics outage")
	}
}
