package e2e

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
)

func TestCloudExportsAndProfileSelection(t *testing.T) {
	for _, selection := range []struct {
		name  string
		types []string
	}{{"heap_only", []string{"heap"}}, {"profiles_disabled", []string{}}} {
		t.Run(selection.name, func(t *testing.T) {
			const key = "fake-e2e-cloud-key"
			grpcServer := grpc.NewServer()
			sink := &traceCollector{requests: make(chan grpcCapture, 16)}
			collector.RegisterTraceServiceServer(grpcServer, sink)
			requests := make(chan exportRequest, 64)
			handlerErrors := make(chan error, 1)
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
					grpcServer.ServeHTTP(w, r)
					return
				}
				body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
				if err != nil {
					select {
					case handlerErrors <- err:
					default:
					}
					http.Error(w, "body read failed", 400)
					return
				}
				request := exportRequest{Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
				select {
				case requests <- request:
					w.WriteHeader(http.StatusOK)
				case <-r.Context().Done():
				}
			})
			server := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
			t.Cleanup(func() { grpcServer.Stop(); server.Close() })
			app := startApp(t, fixtureConfig{CloudEndpoint: server.URL, CloudAPIKey: key, CloudProfileTypes: selection.types, CloudPushIntervalMillis: 200, ProfileIntervalMillis: 400, CPUWindowMillis: 100})
			if status := getBehavior(t, app.URL+"/ok/cloud").StatusCode; status != http.StatusNoContent {
				t.Fatalf("application status: %d", status)
			}
			heapSeen := false
			metricSeen := false
			metricPushes := 0
			inspect := func(request exportRequest) {
				if request.Header.Get("Authorization") != "Bearer "+key {
					t.Fatalf("cloud HTTP authentication: %v", request.Header)
				}
				switch request.Path {
				case "/v1/metrics":
					metricPushes++
					if !strings.HasPrefix(request.Header.Get("Content-Type"), "text/plain; version=0.0.4") {
						t.Fatalf("metrics content type: %v", request.Header)
					}
					if value, count := behaviorMetric(string(request.Body), "http_request_count", "/ok/{id}"); count == 1 && value == 1 {
						metricSeen = true
					}
				case "/v1/profiles":
					if len(selection.types) == 0 {
						t.Fatal("disabled cloud profiling exported a profile")
					}
					decoded := decodeProfileExport(t, request)
					for _, resource := range decoded.ResourceProfiles {
						for _, scope := range resource.ScopeProfiles {
							for _, profile := range scope.Profiles {
								kind := profileKind(t, decoded.Dictionary, profile)
								if kind != "heap" {
									t.Fatalf("CloudProfiles(heap) exported %q", kind)
								}
								validateNativeProfile(t, decoded.Dictionary, profile, kind)
								heapSeen = true
							}
						}
					}
				default:
					t.Fatalf("unexpected cloud HTTP path: %q", request.Path)
				}
			}
			deadline := time.NewTimer(10 * time.Second)
			defer deadline.Stop()
			for !metricSeen || (len(selection.types) > 0 && !heapSeen) || metricPushes < 3 {
				select {
				case request := <-requests:
					inspect(request)
				case err := <-handlerErrors:
					t.Fatal(err)
				case <-deadline.C:
					t.Fatalf("cloud exports deadline: metrics=%t heap=%t pushes=%d\n%s", metricSeen, heapSeen, metricPushes, app.Logs())
				}
			}
			app.Stop(t)
			select {
			case capture := <-sink.requests:
				if got := capture.metadata.Get("x-bedrock-key"); len(got) != 1 || got[0] != key {
					t.Fatalf("cloud gRPC authentication: %v", capture.metadata)
				}
				spans := validateGRPCRequest(t, capture.request)
				if len(spans) != 1 || behaviorStringAttr(spans[0], "http.path") != "/ok/{id}" {
					t.Fatalf("cloud request spans: %v", spans)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("cloud shutdown did not flush trace\n%s", app.Logs())
			}
			for {
				select {
				case request := <-requests:
					inspect(request)
				case err := <-handlerErrors:
					t.Fatal(err)
				default:
					return
				}
			}
		})
	}
}
