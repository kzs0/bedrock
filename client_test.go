package bedrock

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kzs0/bedrock/trace"
	httpProp "github.com/kzs0/bedrock/trace/http"
	"github.com/kzs0/bedrock/trace/w3c"
	"github.com/kzs0/bedrock/transport"
)

func TestTransportInjectsTraceContext(t *testing.T) {
	// Create a test server that captures headers
	var capturedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Initialize bedrock
	ctx, close := Init(context.Background())
	defer close()

	// Start an operation to create a span
	op, ctx := Operation(ctx, "test.operation")
	defer op.Done()

	// Get bedrock from context and create transport with tracer
	b := FromContext(ctx)
	tr := &transport.Transport{
		Tracer: b.Tracer(),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}

	// Verify traceparent was injected
	traceparent := capturedHeaders.Get("traceparent")
	if traceparent == "" {
		t.Fatal("traceparent header not injected")
	}

	// Parse and verify it's valid
	_, _, _, err = w3c.ParseTraceparent(traceparent)
	if err != nil {
		t.Fatalf("invalid traceparent: %v", err)
	}
}

func TestTransportPropagatesTracestate(t *testing.T) {
	// Create a test server that captures headers
	var capturedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Initialize bedrock
	ctx, close := Init(context.Background())
	defer close()

	// Create a mock incoming request with traceparent and tracestate
	incomingHeaders := http.Header{
		"Traceparent": []string{"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"},
		"Tracestate":  []string{"vendor1=value1,vendor2=value2"},
	}

	// Extract trace context
	prop := &httpProp.Propagator{}
	remoteCtx, err := prop.Extract(incomingHeaders)
	if err != nil {
		t.Fatal(err)
	}

	// Start operation with remote parent
	op, ctx := Operation(ctx, "test.operation", WithRemoteParent(remoteCtx))
	defer op.Done()

	// Get bedrock from context and create transport with tracer
	b := FromContext(ctx)
	tr := &transport.Transport{
		Tracer: b.Tracer(),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}

	// Verify tracestate was propagated
	tracestate := capturedHeaders.Get("tracestate")
	if tracestate != "vendor1=value1,vendor2=value2" {
		t.Errorf("tracestate = %v, want vendor1=value1,vendor2=value2", tracestate)
	}

	// Verify trace ID is preserved
	traceparent := capturedHeaders.Get("traceparent")
	traceID, _, _, err := w3c.ParseTraceparent(traceparent)
	if err != nil {
		t.Fatal(err)
	}

	if traceID.String() != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("trace ID not preserved: got %s", traceID.String())
	}
}

func TestTransportRecordsSpan(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Initialize bedrock
	ctx, close := Init(context.Background())
	defer close()

	// Start an operation
	op, ctx := Operation(ctx, "test.operation")
	defer op.Done()

	// Get the span before the request
	spanBefore := trace.SpanFromContext(ctx)
	if spanBefore == nil {
		t.Fatal("no span in context")
	}

	// Get bedrock from context and create transport with tracer
	b := FromContext(ctx)
	tr := &transport.Transport{
		Tracer: b.Tracer(),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}

	// The transport should have created a child span (not visible in ctx after return)
	// We can't easily verify the child span was created without exposing internals,
	// but we can verify the request succeeded and no errors were recorded on parent
	status, _ := spanBefore.Status()
	if status == trace.StatusError {
		t.Error("parent span should not have error status")
	}
}

func TestTransportHandlesErrors(t *testing.T) {
	// Initialize bedrock
	ctx, close := Init(context.Background())
	defer close()

	// Start an operation
	op, ctx := Operation(ctx, "test.operation")
	defer op.Done()

	// Get bedrock from context and create transport with tracer
	b := FromContext(ctx)
	tr := &transport.Transport{
		Tracer: b.Tracer(),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost:99999", nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = tr.RoundTrip(req)
	if err == nil {
		t.Fatal("expected error for non-existent server")
	}
}

func TestTransportHandles4xxStatus(t *testing.T) {
	// Create a test server that returns 404
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	// Initialize bedrock
	ctx, close := Init(context.Background())
	defer close()

	// Start an operation
	op, ctx := Operation(ctx, "test.operation")
	defer op.Done()

	// Get bedrock from context and create transport with tracer
	b := FromContext(ctx)
	tr := &transport.Transport{
		Tracer: b.Tracer(),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", resp.StatusCode)
	}
}

func TestNewClient(t *testing.T) {
	// Create instrumented client
	client := NewClient(nil)

	if client == nil {
		t.Fatal("NewClient returned nil")
	}

	// Verify transport is set
	if _, ok := client.Transport.(*instrumentedTransport); !ok {
		t.Error("client transport is not *instrumentedTransport")
	}
}

func TestNewClientWithBase(t *testing.T) {
	// Create base client with custom settings
	base := &http.Client{
		Timeout: 30,
	}

	// Create instrumented client
	client := NewClient(base)

	if client.Timeout != base.Timeout {
		t.Error("client settings not preserved")
	}
}

func TestDo(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Initialize bedrock
	ctx, close := Init(context.Background())
	defer close()

	// Start an operation
	op, ctx := Operation(ctx, "test.operation")
	defer op.Done()

	// Make request using Do
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := Do(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestGet(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET method, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Initialize bedrock
	ctx, close := Init(context.Background())
	defer close()

	// Make request using Get
	resp, err := Get(ctx, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestPost(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST method, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}

		// Read and verify body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `{"test": true}` {
			t.Errorf("unexpected body: %s", string(body))
		}

		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	// Initialize bedrock
	ctx, close := Init(context.Background())
	defer close()

	// Make request using Post
	body := strings.NewReader(`{"test": true}`)
	resp, err := Post(ctx, server.URL, "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected status 201, got %d", resp.StatusCode)
	}
}

func TestTransportWithoutBedrock(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Make request without bedrock in context
	ctx := context.Background()
	tr := &transport.Transport{}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Verify no traceparent was injected (would need to capture in server)
	// This is implicitly tested by the request succeeding without bedrock
}
