package bedrock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kzs0/bedrock/trace"
)

func TestHTTPMiddlewareSpanKind(t *testing.T) {
	ctx, close := Init(context.Background(), WithConfig(Config{Service: "kind-test", TraceSampleRate: 1}))
	defer close()
	handler := HTTPMiddleware(ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		span := trace.SpanFromContext(r.Context())
		if span == nil || span.Kind() != trace.SpanKindServer {
			t.Fatalf("inbound span: %v", span)
		}
		op, child := Operation(r.Context(), "child")
		defer op.Done()
		if got := trace.SpanFromContext(child).Kind(); got != trace.SpanKindInternal {
			t.Fatalf("child kind %v", got)
		}
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}
