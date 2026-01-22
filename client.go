package bedrock

import (
	"context"
	"io"
	"net/http"

	"github.com/kzs0/bedrock/transport"
)

// instrumentedTransport wraps a base RoundTripper and gets the tracer from context.
type instrumentedTransport struct {
	base http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t *instrumentedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	// Get bedrock from context
	b := FromContext(ctx)

	// Create transport with tracer if bedrock is available
	tr := &transport.Transport{
		Base: t.base,
	}

	if b != nil && !b.IsNoop() {
		tr.Tracer = b.Tracer()
	}

	return tr.RoundTrip(req)
}

// NewClient creates an http.Client with bedrock instrumentation.
// The client automatically injects trace context and creates spans for requests.
// The tracer is obtained from the context when requests are made.
//
// Usage:
//
//	client := bedrock.NewClient(nil)  // Uses default HTTP client settings
//	resp, err := client.Get("https://api.example.com/users")
//
// Or with custom settings:
//
//	base := &http.Client{Timeout: 30 * time.Second}
//	client := bedrock.NewClient(base)
func NewClient(base *http.Client) *http.Client {
	if base == nil {
		base = &http.Client{}
	}

	return &http.Client{
		Transport:     &instrumentedTransport{base: base.Transport},
		CheckRedirect: base.CheckRedirect,
		Jar:           base.Jar,
		Timeout:       base.Timeout,
	}
}

// Do executes an HTTP request with bedrock instrumentation.
// This is a convenience function that creates a one-time instrumented client.
//
// For better performance with multiple requests, create a client once with NewClient
// and reuse it.
//
// Usage:
//
//	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.example.com/users", nil)
//	resp, err := bedrock.Do(ctx, req)
func Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	// Set context on request
	req = req.WithContext(ctx)

	// Get bedrock from context
	b := FromContext(ctx)

	// Create instrumented transport
	tr := &transport.Transport{}

	if b != nil && !b.IsNoop() {
		tr.Tracer = b.Tracer()
	}

	return tr.RoundTrip(req)
}

// Get is a convenience function for GET requests with bedrock instrumentation.
//
// Usage:
//
//	resp, err := bedrock.Get(ctx, "https://api.example.com/users")
func Get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return Do(ctx, req)
}

// Post is a convenience function for POST requests with bedrock instrumentation.
//
// Usage:
//
//	body := strings.NewReader(`{"name": "John"}`)
//	resp, err := bedrock.Post(ctx, "https://api.example.com/users", "application/json", body)
func Post(ctx context.Context, url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return Do(ctx, req)
}
