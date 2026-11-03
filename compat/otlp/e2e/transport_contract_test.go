package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kzs0/bedrock/trace/w3c"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type transportTestOptions struct {
	Endpoint string
	Protocol string
	Insecure bool
	Headers  map[string]string
	Timeout  int
}

func transportFixture(t *testing.T, options transportTestOptions, environment map[string]string) *fixtureApp {
	t.Helper()
	raw, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	return startApp(t, fixtureConfig{Scenario: "transport", ScenarioConfig: raw, Environment: environment})
}

func transportExport(t *testing.T, app *fixtureApp, query string) string {
	t.Helper()
	response, err := (&http.Client{Timeout: 15 * time.Second}).Get(app.URL + "/export?" + query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var result struct {
		Error string
		Count int
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || result.Count < 1 {
		t.Fatalf("export result: %d %+v", response.StatusCode, result)
	}
	return result.Error
}

func TestTransportTraceTLSAndAuthentication(t *testing.T) {
	requests := make(chan exportRequest, 8)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- exportRequest{Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
		if r.Header.Get("Authorization") != "Bearer trace-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "collector.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, token, want string
		trusted           bool
	}{{"trusted", "Bearer trace-test", "", true}, {"rejected_auth", "Bearer wrong", "401", true}, {"untrusted", "Bearer trace-test", "certificate", false}} {
		t.Run(tc.name, func(t *testing.T) {
			environment := map[string]string{"SSL_CERT_DIR": t.TempDir()}
			if tc.trusted {
				environment["SSL_CERT_FILE"] = ca
			}
			app := transportFixture(t, transportTestOptions{Endpoint: server.URL + "/v1/traces", Headers: map[string]string{"Authorization": tc.token}, Timeout: 1000}, environment)
			err := transportExport(t, app, "n=2")
			if (tc.want == "" && err != "") || (tc.want != "" && !strings.Contains(err, tc.want)) {
				t.Fatalf("TLS/auth error: %q, want %q", err, tc.want)
			}
			app.Stop(t)
			if tc.trusted {
				request := nextBehaviorExport(t, requests)
				if request.Header.Get("Authorization") != tc.token || len(behaviorSpans(t, request)) != 2 {
					t.Fatal("TLS delivery lost auth or spans")
				}
			} else {
				select {
				case request := <-requests:
					t.Fatalf("untrusted TLS reached handler: %v", request.Header)
				default:
				}
			}
		})
	}
}

type transportAuthCollector struct {
	collector.UnimplementedTraceServiceServer
	requests chan *collector.ExportTraceServiceRequest
	tokens   chan string
}

func (c *transportAuthCollector) Export(ctx context.Context, request *collector.ExportTraceServiceRequest) (*collector.ExportTraceServiceResponse, error) {
	values, _ := metadata.FromIncomingContext(ctx)
	token := strings.Join(values.Get("authorization"), ",")
	c.tokens <- token
	c.requests <- request
	if token != "Bearer grpc-test" {
		return nil, status.Error(codes.PermissionDenied, "invalid test token")
	}
	return &collector.ExportTraceServiceResponse{}, nil
}

func TestTransportGRPCTLSAndAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name, token, want string
		trusted           bool
	}{{"trusted", "Bearer grpc-test", "", true}, {"rejected_auth", "Bearer wrong", "gRPC status 7", true}, {"untrusted", "Bearer grpc-test", "certificate", false}} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &transportAuthCollector{requests: make(chan *collector.ExportTraceServiceRequest, 1), tokens: make(chan string, 1)}
			grpcServer := grpc.NewServer()
			collector.RegisterTraceServiceServer(grpcServer, sink)
			defer grpcServer.Stop()
			server := httptest.NewUnstartedServer(grpcServer)
			server.EnableHTTP2 = true
			server.StartTLS()
			defer server.Close()
			ca := filepath.Join(t.TempDir(), "collector.pem")
			environment := map[string]string{"SSL_CERT_DIR": t.TempDir(), "SSL_CERT_FILE": ca}
			if tc.trusted {
				if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			app := transportFixture(t, transportTestOptions{Endpoint: strings.TrimPrefix(server.URL, "https://"), Protocol: "grpc", Headers: map[string]string{"authorization": tc.token}, Timeout: 1000}, environment)
			err := transportExport(t, app, "n=2")
			if (tc.want == "" && err != "") || (tc.want != "" && !strings.Contains(err, tc.want)) {
				t.Fatalf("gRPC TLS/auth error %q, want %q", err, tc.want)
			}
			app.Stop(t)
			if tc.trusted {
				select {
				case request := <-sink.requests:
					if len(transportValidateGRPCRequest(t, request)) != 2 || <-sink.tokens != tc.token {
						t.Fatal("gRPC TLS delivery lost spans or authentication")
					}
				default:
					t.Fatal("gRPC TLS collector did not receive request")
				}
			} else {
				select {
				case request := <-sink.requests:
					t.Fatalf("untrusted gRPC collector received request: %v", request)
				default:
				}
			}
		})
	}
}

func TestTransportHTTPExportResponseContract(t *testing.T) {
	for _, status := range []int{200, 202, 204, 400, 401, 403, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			receiver := newReceiver(t)
			receiver.SetHandler(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "deliberately-not-a-protobuf-response")
			})
			app := transportFixture(t, transportTestOptions{Endpoint: receiver.URL + "/v1/traces", Timeout: 1000}, nil)
			err := transportExport(t, app, "n=1")
			if status < 300 {
				if err != "" {
					t.Fatalf("successful status rejected: %q", err)
				}
			} else if !strings.Contains(err, fmt.Sprint(status)) {
				t.Fatalf("status %d omitted: %q", status, err)
			}
			app.Stop(t)
			if spans := behaviorSpans(t, nextBehaviorExport(t, receiver.Requests)); len(spans) != 1 || spans[0].Name != "transport.export" {
				t.Fatalf("trace payload: %v", spans)
			}
			select {
			case <-receiver.Requests:
				t.Fatal("HTTP trace exporter retried without contract")
			default:
			}
		})
	}
}

func TestTransportTypedZeroAndIntegerBoundaries(t *testing.T) {
	want := map[string]*commonpb.AnyValue{
		"typed.uint.zero":     {Value: &commonpb.AnyValue_IntValue{IntValue: 0}},
		"typed.bool.zero":     {Value: &commonpb.AnyValue_BoolValue{BoolValue: false}},
		"typed.string.zero":   {Value: &commonpb.AnyValue_StringValue{StringValue: ""}},
		"typed.double.zero":   {Value: &commonpb.AnyValue_DoubleValue{DoubleValue: 0}},
		"typed.duration.zero": {Value: &commonpb.AnyValue_IntValue{IntValue: 0}},
		"typed.int.max":       {Value: &commonpb.AnyValue_IntValue{IntValue: math.MaxInt64}},
		"typed.uint.max":      {Value: &commonpb.AnyValue_StringValue{StringValue: "18446744073709551615"}},
	}
	for _, protocol := range []string{"http", "grpc"} {
		t.Run(protocol, func(t *testing.T) {
			var app *fixtureApp
			var receive func() []*tracepb.Span
			if protocol == "http" {
				receiver := newReceiver(t)
				app = transportFixture(t, transportTestOptions{Endpoint: receiver.URL + "/v1/traces"}, nil)
				receive = func() []*tracepb.Span { return behaviorSpans(t, nextBehaviorExport(t, receiver.Requests)) }
			} else {
				address, captures := transportRawBackend(t, transportRawCase{response: func(f *http2.Framer, id uint32) {
					transportResponse(f, id, hpack.HeaderField{Name: ":status", Value: "200"}, hpack.HeaderField{Name: "content-type", Value: "application/grpc"}, hpack.HeaderField{Name: "grpc-status", Value: "0"})
				}})
				app = transportFixture(t, transportTestOptions{Endpoint: address, Protocol: "grpc", Insecure: true}, nil)
				receive = func() []*tracepb.Span {
					select {
					case request := <-captures:
						return transportValidateGRPCRequest(t, request)
					case <-time.After(3 * time.Second):
						t.Fatal("gRPC typed export missing")
						return nil
					}
				}
			}
			if err := transportExport(t, app, "n=1&typed=true"); err != "" {
				t.Fatal(err)
			}
			app.Stop(t)
			spans := receive()
			if len(spans) != 1 {
				t.Fatalf("typed span count %d", len(spans))
			}
			actual := make(map[string]*commonpb.AnyValue)
			for _, attribute := range spans[0].Attributes {
				actual[attribute.Key] = attribute.Value
			}
			for key, value := range want {
				if !proto.Equal(actual[key], value) {
					t.Errorf("%s = %v, want %v", key, actual[key], value)
				}
			}
		})
	}
}

func TestTransportTraceContextValidation(t *testing.T) {
	app := transportFixture(t, transportTestOptions{}, nil)
	const valid = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	for _, tc := range []struct {
		name, parent string
		valid        bool
	}{{"sampled", valid, true}, {"unsampled", strings.TrimSuffix(valid, "01") + "00", true}, {"future", "02" + valid[2:] + "-future", true}, {"short", "invalid", false}, {"version_length", "000" + valid[2:], false}, {"version_uppercase", "0A" + valid[2:], false}, {"forbidden_version", "ff" + valid[2:], false}, {"extra_v0", valid + "-extra", false}, {"short_trace_id", strings.Replace(valid, "0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcde", 1), false}, {"bad_trace_id", strings.Replace(valid, "abcdef", "ABCDEF", 1), false}, {"zero_trace_id", strings.Replace(valid, "0123456789abcdef0123456789abcdef", strings.Repeat("0", 32), 1), false}, {"short_span_id", valid[:36] + "-123-01", false}, {"zero_span_id", valid[:36] + "-0000000000000000-01", false}, {"bad_span_id", valid[:36] + "-0123456789abcdeZ-01", false}, {"short_flags", valid[:len(valid)-2] + "1", false}, {"bad_flags", valid[:len(valid)-2] + "0A", false}} {
		t.Run(tc.name, func(t *testing.T) {
			result := transportContext(t, app, tc.parent, []string{"vendor=safe"}, nil)
			if result.Valid != tc.valid || (result.Error == "") != tc.valid {
				t.Fatalf("traceparent validation: %+v", result)
			}
			if tc.valid && (result.Tracestate != "vendor=safe" || result.Sampled != !strings.HasSuffix(tc.parent, "00")) {
				t.Fatalf("accepted context lost state: %+v", result)
			}
		})
	}
	states := []struct {
		name  string
		state []string
		want  string
	}{{"multiple_headers", []string{"vendor=one", "other=two"}, "vendor=one,other=two"}, {"empty", nil, ""}, {"duplicate", []string{"vendor=one,vendor=two"}, ""}, {"invalid_key", []string{"UPPER=one"}, ""}, {"missing_equals", []string{"broken"}, ""}, {"invalid_value", []string{"vendor=one=two"}, ""}, {"empty_value", []string{"vendor="}, ""}, {"long_header", []string{"vendor=" + strings.Repeat("x", 513)}, ""}, {"long_key", []string{strings.Repeat("a", 257) + "=one"}, ""}, {"tenant_key", []string{"123@vendor=one"}, "123@vendor=one"}, {"invalid_tenant", []string{"a@@vendor=one"}, ""}, {"invalid_system", []string{"a@123=one"}, ""}, {"long_tenant", []string{strings.Repeat("a", 242) + "@vendor=one"}, ""}, {"long_system", []string{"a@" + strings.Repeat("a", 15) + "=one"}, ""}, {"many_entries", []string{strings.TrimSuffix(strings.Repeat("a=x,", 33), ",")}, ""}, {"whitespace", []string{" vendor=one , other=two "}, " vendor=one , other=two "}, {"empty_member", []string{"vendor=one,,other=two"}, "vendor=one,,other=two"}}
	for _, tc := range states {
		t.Run(tc.name, func(t *testing.T) {
			result := transportContext(t, app, valid, tc.state, nil)
			if !result.Valid || result.Tracestate != tc.want {
				t.Fatalf("tracestate validation: %+v, want %q", result, tc.want)
			}
		})
	}
	entries := []w3c.Entry{{Key: "vendor", Value: "first"}, {Key: "vendor", Value: "second"}, {Key: "UPPER", Value: "no"}, {Key: "bad", Value: "a=b"}, {Key: "other", Value: "safe"}}
	if result := transportContext(t, app, valid, nil, entries); result.Formatted != "vendor=first,other=safe" {
		t.Fatalf("format filtering: %+v", result)
	}
	entries = nil
	for i := range 40 {
		entries = append(entries, w3c.Entry{Key: fmt.Sprintf("k%d", i), Value: "v"})
	}
	if result := transportContext(t, app, valid, nil, entries); len(strings.Split(result.Formatted, ",")) != 32 {
		t.Fatalf("entry bound: %+v", result)
	}
	entries = []w3c.Entry{{Key: "first", Value: strings.Repeat("x", 256)}, {Key: "second", Value: strings.Repeat("y", 250)}, {Key: "third", Value: "fits"}}
	if result := transportContext(t, app, valid, nil, entries); result.Formatted != "first="+strings.Repeat("x", 256)+",third=fits" {
		t.Fatalf("byte bound and later admission: %+v", result)
	}
}

type transportContextResult struct {
	Valid      bool
	Sampled    bool
	Tracestate string
	Formatted  string
	Error      string
}

func transportContext(t *testing.T, app *fixtureApp, parent string, state []string, format []w3c.Entry) transportContextResult {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"Traceparent": parent, "Tracestate": state, "Format": format})
	response, err := http.Post(app.URL+"/context", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var result transportContextResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestTransportFractionalSampling(t *testing.T) {
	app := transportFixture(t, transportTestOptions{}, nil)
	for _, rate := range []float64{-1, 0, .5, 1, 2} {
		response, err := http.Get(fmt.Sprintf("%s/sampling?rate=%g", app.URL, rate))
		if err != nil {
			t.Fatal(err)
		}
		var result struct{ Accepted, Total int }
		err = json.NewDecoder(response.Body).Decode(&result)
		_ = response.Body.Close()
		if err != nil || result.Total != 10000 {
			t.Fatalf("sampling result: %+v, %v", result, err)
		}
		if (rate <= 0 && result.Accepted != 0) || (rate >= 1 && result.Accepted != result.Total) || (rate == .5 && (result.Accepted < 4500 || result.Accepted > 5500)) {
			t.Fatalf("sampling %g: %+v", rate, result)
		}
	}
}

func transportHPACK(fields ...hpack.HeaderField) []byte {
	var output bytes.Buffer
	encoder := hpack.NewEncoder(&output)
	encoder.SetMaxDynamicTableSize(0)
	for _, field := range fields {
		_ = encoder.WriteField(field)
	}
	return output.Bytes()
}

func transportResponse(f *http2.Framer, id uint32, fields ...hpack.HeaderField) {
	_ = f.WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: transportHPACK(fields...), EndStream: true, EndHeaders: true})
}

type transportRawCase struct {
	name      string
	want      string
	setup     func(*http2.Framer)
	handshake func(*http2.Framer)
	response  func(*http2.Framer, uint32)
	early     bool
	timeout   int
}

func TestTransportGRPCProtocolBoundaries(t *testing.T) {
	status := hpack.HeaderField{Name: ":status", Value: "200"}
	typ := hpack.HeaderField{Name: "content-type", Value: "application/grpc"}
	ok := hpack.HeaderField{Name: "grpc-status", Value: "0"}
	cases := []transportRawCase{
		{name: "valid", response: func(f *http2.Framer, id uint32) { transportResponse(f, id, status, typ, ok) }},
		{name: "handshake_control_frames", setup: func(f *http2.Framer) {
			_ = f.WriteSettingsAck()
			_ = f.WriteWindowUpdate(0, 16)
			_ = f.WritePing(false, [8]byte{1, 2, 3})
			for {
				frame, err := f.ReadFrame()
				if err != nil {
					return
				}
				if ping, ok := frame.(*http2.PingFrame); ok && ping.IsAck() && ping.Data == [8]byte{1, 2, 3} {
					break
				}
			}
			_ = f.WritePing(true, [8]byte{4, 5, 6})
			_ = f.WriteRawFrame(0xff, 0, 0, nil)
		}, response: func(f *http2.Framer, id uint32) { transportResponse(f, id, status, typ, ok) }},
		{name: "response_control_frames", response: func(f *http2.Framer, id uint32) {
			_ = f.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: 4096}, http2.Setting{ID: http2.SettingMaxFrameSize, Val: 32768})
			_ = f.WriteSettingsAck()
			_ = f.WritePing(false, [8]byte{7, 8, 9})
			for {
				frame, err := f.ReadFrame()
				if err != nil {
					return
				}
				if ping, ok := frame.(*http2.PingFrame); ok && ping.IsAck() && ping.Data == [8]byte{7, 8, 9} {
					break
				}
			}
			_ = f.WritePing(true, [8]byte{1, 2, 3})
			_ = f.WriteWindowUpdate(0, 10)
			_ = f.WriteWindowUpdate(id, 10)
			transportResponse(f, id, status, typ, ok)
		}},
		{name: "padded_priority_and_data", response: func(f *http2.Framer, id uint32) {
			payload := append([]byte{2, 0, 0, 0, 0, 15}, transportHPACK(status, typ)...)
			payload = append(payload, 0, 0)
			_ = f.WriteRawFrame(http2.FrameHeaders, http2.FlagHeadersPadded|http2.FlagHeadersPriority|http2.FlagHeadersEndHeaders, id, payload)
			_ = f.WriteDataPadded(id, false, []byte{0, 0, 0, 0, 0}, []byte{0, 0})
			credits := 0
			for credits < 2 {
				frame, err := f.ReadFrame()
				if err != nil {
					return
				}
				if update, ok := frame.(*http2.WindowUpdateFrame); ok && update.Increment == 8 {
					credits++
				}
			}
			transportResponse(f, id, ok)
		}},
		{name: "response_data_and_flow_credit", response: func(f *http2.Framer, id uint32) {
			_ = f.WriteHeaders(http2.HeadersFrameParam{StreamID: id, EndHeaders: true, BlockFragment: transportHPACK(status, typ)})
			_ = f.WriteData(id, false, []byte{0, 0, 0, 0, 0})
			connectionCredit, streamCredit := false, false
			for !connectionCredit || !streamCredit {
				frame, err := f.ReadFrame()
				if err != nil {
					return
				}
				if update, ok := frame.(*http2.WindowUpdateFrame); ok && update.Increment == 5 {
					connectionCredit = connectionCredit || update.StreamID == 0
					streamCredit = streamCredit || update.StreamID == id
				}
			}
			transportResponse(f, id, ok)
		}},
		{name: "early_rejection_without_upload", want: "gRPC status 7", early: true, response: func(f *http2.Framer, id uint32) {
			transportResponse(f, id, status, typ, hpack.HeaderField{Name: "grpc-status", Value: "7"})
		}},
		{name: "response_body_budget", want: "response body exceeds limit", timeout: 10000, response: func(f *http2.Framer, id uint32) {
			_ = f.WriteHeaders(http2.HeadersFrameParam{StreamID: id, EndHeaders: true, BlockFragment: transportHPACK(status, typ)})
			payload := make([]byte, 16384)
			for sent := 0; sent <= 16<<20; {
				n := min(len(payload), (16<<20)+1-sent)
				if err := f.WriteData(id, false, payload[:n]); err != nil {
					return
				}
				sent += n
				credits := 0
				for credits < 2 {
					frame, err := f.ReadFrame()
					if err != nil {
						return
					}
					if update, ok := frame.(*http2.WindowUpdateFrame); ok && update.Increment == uint32(n) {
						credits++
					}
				}
			}
		}},
		{name: "push_promise", want: "PUSH_PROMISE", response: func(f *http2.Framer, id uint32) {
			_ = f.WritePushPromise(http2.PushPromiseParam{StreamID: id, PromiseID: 2, EndHeaders: true, BlockFragment: transportHPACK(hpack.HeaderField{Name: ":method", Value: "GET"})})
		}},
		{name: "zero_window_update", want: "WINDOW_UPDATE", response: func(f *http2.Framer, _ uint32) {
			_ = f.WriteRawFrame(http2.FrameWindowUpdate, 0, 0, []byte{0, 0, 0, 0})
		}},
		{name: "missing_status", want: "missing HTTP status", response: func(f *http2.Framer, id uint32) { transportResponse(f, id, typ, ok) }},
		{name: "bad_http_status", want: "malformed HTTP status", response: func(f *http2.Framer, id uint32) {
			transportResponse(f, id, hpack.HeaderField{Name: ":status", Value: "bad"}, typ, ok)
		}},
		{name: "http_failure", want: "HTTP status 503", response: func(f *http2.Framer, id uint32) {
			transportResponse(f, id, hpack.HeaderField{Name: ":status", Value: "503"}, typ, ok)
		}},
		{name: "duplicate_status", want: "duplicate HTTP status", response: func(f *http2.Framer, id uint32) { transportResponse(f, id, status, status, typ, ok) }},
		{name: "missing_grpc_status", want: "missing grpc-status", response: func(f *http2.Framer, id uint32) { transportResponse(f, id, status, typ) }},
		{name: "bad_grpc_status", want: "malformed grpc-status", response: func(f *http2.Framer, id uint32) {
			transportResponse(f, id, status, typ, hpack.HeaderField{Name: "grpc-status", Value: "17"})
		}},
		{name: "duplicate_grpc_status", want: "duplicate grpc-status", response: func(f *http2.Framer, id uint32) { transportResponse(f, id, status, typ, ok, ok) }},
		{name: "grpc_failure", want: "gRPC status 7", response: func(f *http2.Framer, id uint32) {
			transportResponse(f, id, status, typ, hpack.HeaderField{Name: "grpc-status", Value: "7"})
		}},
		{name: "missing_content_type", want: "content-type", response: func(f *http2.Framer, id uint32) { transportResponse(f, id, status, ok) }},
		{name: "wrong_content_type", want: "content-type", response: func(f *http2.Framer, id uint32) {
			transportResponse(f, id, status, hpack.HeaderField{Name: "content-type", Value: "text/plain"}, ok)
		}},
		{name: "duplicate_content_type", want: "duplicate response content-type", response: func(f *http2.Framer, id uint32) { transportResponse(f, id, status, typ, typ, ok) }},
		{name: "reset", want: "RST_STREAM", response: func(f *http2.Framer, id uint32) { _ = f.WriteRSTStream(id, http2.ErrCodeRefusedStream) }},
		{name: "goaway_unprocessed", want: "not processed", response: func(f *http2.Framer, _ uint32) { _ = f.WriteGoAway(0, http2.ErrCodeNo, nil) }},
		{name: "goaway_accepted", response: func(f *http2.Framer, id uint32) {
			_ = f.WriteGoAway(id, http2.ErrCodeNo, nil)
			transportResponse(f, id, status, typ, ok)
		}},
		{name: "data_before_headers", want: "before response headers", response: func(f *http2.Framer, id uint32) { _ = f.WriteData(id, true, []byte("bad")) }},
		{name: "unexpected_continuation", want: "unexpected CONTINUATION", response: func(f *http2.Framer, id uint32) { _ = f.WriteContinuation(id, true, []byte{0x88}) }},
		{name: "invalid_hpack", want: "decode response headers", response: func(f *http2.Framer, id uint32) {
			_ = f.WriteHeaders(http2.HeadersFrameParam{StreamID: id, EndStream: true, EndHeaders: true, BlockFragment: []byte{0xff, 0xff, 0xff, 0xff}})
		}},
		{name: "nonterminal_grpc_status", want: "non-terminal", response: func(f *http2.Framer, id uint32) {
			_ = f.WriteHeaders(http2.HeadersFrameParam{StreamID: id, EndHeaders: true, BlockFragment: transportHPACK(status, typ, ok)})
			_ = f.WriteData(id, true, nil)
		}},
		{name: "fragmented_headers", response: func(f *http2.Framer, id uint32) {
			block := transportHPACK(status, typ, ok)
			_ = f.WriteHeaders(http2.HeadersFrameParam{StreamID: id, EndStream: true, BlockFragment: block[:2]})
			_ = f.WriteContinuation(id, true, block[2:])
		}},
		{name: "interrupted_headers", want: "expected CONTINUATION", response: func(f *http2.Framer, id uint32) {
			_ = f.WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: []byte{0x88}})
			_ = f.WriteData(id, true, nil)
		}},
		{name: "settings_enable_push", want: "SETTINGS_ENABLE_PUSH", handshake: func(f *http2.Framer) { _ = f.WriteSettings(http2.Setting{ID: http2.SettingEnablePush, Val: 1}) }},
		{name: "handshake_zero_credit", want: "zero increment", handshake: func(f *http2.Framer) { _ = f.WriteRawFrame(http2.FrameWindowUpdate, 0, 0, make([]byte, 4)) }},
		{name: "handshake_credit_overflow", want: "overflow", handshake: func(f *http2.Framer) { _ = f.WriteWindowUpdate(0, 0x7fffffff) }},
		{name: "handshake_unopened_stream_credit", want: "unopened stream", handshake: func(f *http2.Framer) { _ = f.WriteWindowUpdate(1, 1) }},
		{name: "connection_credit_overflow", want: "connection flow-control window overflow", response: func(f *http2.Framer, _ uint32) { _ = f.WriteWindowUpdate(0, 0x7fffffff) }},
		{name: "stream_credit_overflow", want: "stream flow-control window overflow", response: func(f *http2.Framer, id uint32) { _ = f.WriteWindowUpdate(id, 0x7fffffff) }},
		{name: "response_settings_enable_push", want: "SETTINGS_ENABLE_PUSH", response: func(f *http2.Framer, _ uint32) {
			_ = f.WriteSettings(http2.Setting{ID: http2.SettingEnablePush, Val: 0})
		}},
		{name: "response_settings_overflow", want: "overflow after SETTINGS", response: func(f *http2.Framer, id uint32) {
			_ = f.WriteWindowUpdate(id, 0x7fffffff-65535)
			_ = f.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: 0x7fffffff})
		}},
		{name: "settings_invalid_window", want: "SETTINGS_INITIAL_WINDOW_SIZE", handshake: func(f *http2.Framer) {
			_ = f.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: 1 << 31})
		}},
		{name: "settings_invalid_frame", want: "SETTINGS_MAX_FRAME_SIZE", handshake: func(f *http2.Framer) { _ = f.WriteSettings(http2.Setting{ID: http2.SettingMaxFrameSize, Val: 1}) }},
		{name: "handshake_goaway", want: "GOAWAY", handshake: func(f *http2.Framer) { _ = f.WriteGoAway(0, http2.ErrCodeEnhanceYourCalm, nil) }},
		{name: "handshake_stream", want: "before server SETTINGS", handshake: func(f *http2.Framer) { _ = f.WriteData(1, true, nil) }},
	}
	for _, malformed := range []struct {
		name, want string
		typ        http2.FrameType
		flags      http2.Flags
		stream     uint32
		payload    []byte
	}{
		{"data_stream_zero", "stream 0", http2.FrameData, 0, 0, nil},
		{"reset_short", "invalid RST_STREAM", http2.FrameRSTStream, 0, 1, []byte{0}},
		{"settings_stream", "invalid SETTINGS", http2.FrameSettings, 0, 1, nil},
		{"settings_short", "invalid SETTINGS", http2.FrameSettings, 0, 0, []byte{0}},
		{"settings_ack_payload", "invalid SETTINGS", http2.FrameSettings, http2.FlagSettingsAck, 0, make([]byte, 6)},
		{"push_stream_zero", "invalid PUSH_PROMISE", http2.FramePushPromise, 0, 0, make([]byte, 4)},
		{"ping_short", "invalid PING", http2.FramePing, 0, 0, []byte{0}},
		{"goaway_short", "invalid GOAWAY", http2.FrameGoAway, 0, 0, []byte{0}},
		{"window_short", "invalid WINDOW_UPDATE", http2.FrameWindowUpdate, 0, 0, []byte{0}},
		{"oversized_frame", "exceeds maximum", http2.FrameData, 0, 1, make([]byte, 16385)},
		{"padding_too_long", "invalid padding", http2.FrameData, http2.FlagDataPadded, 1, []byte{5}},
		{"priority_short", "PRIORITY too short", http2.FrameHeaders, http2.FlagHeadersPriority, 1, []byte{0}},
	} {
		cases = append(cases, transportRawCase{name: malformed.name, want: malformed.want, response: func(f *http2.Framer, _ uint32) {
			_ = f.WriteRawFrame(malformed.typ, malformed.flags, malformed.stream, malformed.payload)
		}})
	}
	for _, malformed := range []struct {
		name, want string
		block      []byte
	}{
		{"hpack_zero_index", "zero index", []byte{0x80}},
		{"hpack_dynamic_index", "dynamic index", []byte{0xbe}},
		{"hpack_incremental_truncated", "incremental literal", []byte{0x40}},
		{"hpack_literal_truncated", "literal", []byte{0}},
		{"hpack_size_truncated", "dynamic table size update", []byte{0x3f}},
		{"hpack_size_nonzero", "exceeds configured maximum", []byte{0x21}},
		{"hpack_size_after_header", "after header field", []byte{0x88, 0x20}},
		{"hpack_bad_name_index", "dynamic", []byte{0x0f, 0x2f, 0}},
		{"hpack_integer_overflow", "overflow", []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}},
		{"hpack_string_truncated", "string", []byte{0, 10, 'a'}},
		{"hpack_name_integer_truncated", "truncated integer", []byte{0x0f}},
		{"hpack_value_missing", "value", []byte{0, 1, 'x'}},
		{"hpack_string_integer_truncated", "truncated integer", []byte{0, 0x7f}},
		{"hpack_huffman_eos", "Huffman", []byte{0, 0x84, 0xff, 0xff, 0xff, 0xff, 0}},
		{"hpack_huffman_padding", "Huffman", []byte{0, 0x81, 0xff, 0}},
		{"hpack_indexed_budget", "header list exceeds", bytes.Repeat([]byte{0x88}, 2000)},
		{"hpack_literal_budget", "header list exceeds", append(bytes.Repeat([]byte{0x88}, 1559), 0, 0, 0, 0, 0, 0)},
	} {
		cases = append(cases, transportRawCase{name: malformed.name, want: malformed.want, response: func(f *http2.Framer, id uint32) {
			_ = f.WriteHeaders(http2.HeadersFrameParam{StreamID: id, EndStream: true, EndHeaders: true, BlockFragment: malformed.block})
		}})
	}
	cases = append(cases, transportRawCase{name: "decoded_header_budget", want: "budget", response: func(f *http2.Framer, id uint32) {
		fields := []hpack.HeaderField{status, typ, ok}
		for range 64 {
			fields = append(fields, hpack.HeaderField{Name: "x-padding", Value: strings.Repeat("a", 1000)})
		}
		transportFragmentedResponse(f, id, transportHPACK(fields...))
	}})
	cases = append(cases, transportRawCase{name: "encoded_header_budget", want: "header block exceeds limit", response: func(f *http2.Framer, id uint32) {
		transportFragmentedResponse(f, id, bytes.Repeat([]byte{0x88}, (64<<10)+1))
	}})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			address, captures := transportRawBackend(t, tc)
			timeout := tc.timeout
			if timeout == 0 {
				timeout = 1000
			}
			app := transportFixture(t, transportTestOptions{Endpoint: address, Protocol: "grpc", Insecure: true, Timeout: timeout}, nil)
			err := transportExport(t, app, "n=2")
			if (tc.want == "" && err != "") || (tc.want != "" && !strings.Contains(err, tc.want)) {
				t.Fatalf("protocol result %q, want %q", err, tc.want)
			}
			app.Stop(t)
			if tc.handshake == nil && !tc.early {
				select {
				case request := <-captures:
					if len(transportValidateGRPCRequest(t, request)) != 2 {
						t.Fatal("request lost spans")
					}
				default:
					t.Fatal("protocol fault did not receive real export")
				}
			} else if tc.early {
				select {
				case request := <-captures:
					t.Fatalf("early rejection still uploaded spans: %v", request)
				default:
				}
			}
		})
	}
}

func transportFragmentedResponse(f *http2.Framer, id uint32, block []byte) {
	first := min(len(block), 16384)
	_ = f.WriteHeaders(http2.HeadersFrameParam{StreamID: id, EndStream: true, EndHeaders: first == len(block), BlockFragment: block[:first]})
	for remaining := block[first:]; len(remaining) > 0; {
		n := min(len(remaining), 16384)
		_ = f.WriteContinuation(id, n == len(remaining), remaining[:n])
		remaining = remaining[n:]
	}
}

func transportValidateGRPCRequest(t *testing.T, request *collector.ExportTraceServiceRequest) []*tracepb.Span {
	t.Helper()
	if len(request.ResourceSpans) != 1 || len(request.ResourceSpans[0].ScopeSpans) != 1 {
		t.Fatalf("gRPC resource/scope envelope: %v", request)
	}
	resource := request.ResourceSpans[0]
	serviceFound := false
	for _, attribute := range resource.GetResource().GetAttributes() {
		if attribute.Key == "service.name" && attribute.Value.GetStringValue() == "transport-contract" {
			serviceFound = true
		}
	}
	if !serviceFound {
		t.Fatalf("gRPC service resource: %v", resource.Resource)
	}
	spans := resource.ScopeSpans[0].Spans
	seen := make(map[string]bool)
	for index, span := range spans {
		id := string(span.TraceId) + string(span.SpanId)
		if len(span.TraceId) != 16 || len(span.SpanId) != 8 || bytes.Equal(span.TraceId, make([]byte, 16)) || bytes.Equal(span.SpanId, make([]byte, 8)) || seen[id] {
			t.Fatalf("gRPC identifiers: %v", span)
		}
		seen[id] = true
		if span.Name != "transport.export" || span.Kind != tracepb.Span_SPAN_KIND_PRODUCER || span.StartTimeUnixNano == 0 || span.EndTimeUnixNano < span.StartTimeUnixNano || span.Status.GetCode() != tracepb.Status_STATUS_CODE_OK || behaviorIntAttr(span, "fixture.index") != int64(index) || len(span.Events) != 1 || span.Events[0].Name != "fixture.event" {
			t.Fatalf("gRPC span semantics: %v", span)
		}
	}
	return spans
}

func transportRawBackend(t *testing.T, tc transportRawCase) (string, <-chan *collector.ExportTraceServiceRequest) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	captures := make(chan *collector.ExportTraceServiceRequest, 1)
	done := make(chan struct{})
	var conn net.Conn
	var mu sync.Mutex
	go func() {
		defer close(done)
		accepted, err := listener.Accept()
		if err != nil {
			return
		}
		mu.Lock()
		conn = accepted
		mu.Unlock()
		defer func() { _ = accepted.Close() }()
		_ = accepted.SetDeadline(time.Now().Add(15 * time.Second))
		preface := make([]byte, len(http2.ClientPreface))
		if _, err := io.ReadFull(accepted, preface); err != nil || string(preface) != http2.ClientPreface {
			return
		}
		framer := http2.NewFramer(accepted, accepted)
		if tc.handshake != nil {
			tc.handshake(framer)
			return
		}
		if tc.setup != nil {
			tc.setup(framer)
		}
		if tc.early {
			_ = framer.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: 0})
		} else {
			_ = framer.WriteSettings()
		}
		var body []byte
		for {
			frame, err := framer.ReadFrame()
			if err != nil {
				return
			}
			if headers, ok := frame.(*http2.HeadersFrame); ok && tc.early && headers.HeadersEnded() {
				tc.response(framer, headers.StreamID)
				for {
					frame, err := framer.ReadFrame()
					if err != nil {
						return
					}
					if _, ok := frame.(*http2.RSTStreamFrame); ok {
						return
					}
					if data, ok := frame.(*http2.DataFrame); ok {
						request := new(collector.ExportTraceServiceRequest)
						_ = proto.Unmarshal(data.Data(), request)
						captures <- request
						return
					}
				}
			}
			if data, ok := frame.(*http2.DataFrame); ok {
				body = append(body, data.Data()...)
				if data.StreamEnded() {
					request := new(collector.ExportTraceServiceRequest)
					if len(body) >= 5 && proto.Unmarshal(body[5:], request) == nil {
						captures <- request
					}
					tc.response(framer, data.Header().StreamID)
					return
				}
			}
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		if conn != nil {
			_ = conn.Close()
		}
		mu.Unlock()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("raw collector did not stop")
		}
	})
	return listener.Addr().String(), captures
}
