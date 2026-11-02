package transport

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/kzs0/bedrock/trace"
)

type regressionRoundTripper func(*http.Request) (*http.Response, error)

func (f regressionRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type regressionSpanExporter struct{ span *trace.Span }

func (e *regressionSpanExporter) EnqueueSpan(s *trace.Span)                        { e.span = s }
func (e *regressionSpanExporter) ExportSpans(context.Context, []*trace.Span) error { return nil }
func (e *regressionSpanExporter) Shutdown(context.Context) error                   { return nil }
func TestTransportPreservesCallerHeaders(t *testing.T) {
	for _, nilHeader := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing", true: "nil"}[nilHeader], func(t *testing.T) {
			tr := trace.NewTracer(trace.TracerConfig{})
			req, _ := http.NewRequest("GET", "https://example.test/", nil)
			if nilHeader {
				req.Header = nil
			} else {
				req.Header.Set("X-Original", "unchanged")
			}
			before := req.Header.Clone()
			transport := &Transport{Tracer: tr, Base: regressionRoundTripper(func(out *http.Request) (*http.Response, error) {
				if out.Header.Get("traceparent") == "" {
					t.Error("outgoing request lacks traceparent")
				}
				if out == req {
					t.Error("original request passed to transport")
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: http.NoBody}, nil
			})}
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Errorf("request panicked: %v", recovered)
				}
			}()
			resp, err := transport.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if !reflect.DeepEqual(req.Header, before) {
				t.Fatalf("caller headers changed: %#v", req.Header)
			}
		})
	}
}
func TestHTTPSpanURLsExcludeSecrets(t *testing.T) {
	exp := &regressionSpanExporter{}
	tracer := trace.NewTracer(trace.TracerConfig{Exporter: exp})
	req, _ := http.NewRequest("GET", "https://private-user:private-password@example.test/api?token=private-token#private-fragment", nil)
	original := req.URL.String()
	transport := &Transport{Tracer: tracer, Base: regressionRoundTripper(func(out *http.Request) (*http.Response, error) {
		if out.URL.String() != original {
			t.Error("redaction changed request destination")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: http.NoBody}, nil
	})}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if exp.span == nil {
		t.Fatal("client span not exported")
	}
	for _, a := range exp.span.Attrs().Attrs() {
		for _, secret := range []string{"private-user", "private-password", "private-token", "private-fragment"} {
			if strings.Contains(a.Value.String(), secret) {
				t.Errorf("attribute %q exposed %s", a.Key, secret)
			}
		}
	}
}
