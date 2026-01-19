package bedrock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kzs0/bedrock/attr"
)

func TestHTTPMiddleware_PreservesRequestContext(t *testing.T) {
	// Setup: Create bedrock and base context
	ctx, close := Init(context.Background(),
		WithConfig(Config{ServiceName: "test-service"}),
	)
	defer close()

	// Test: Add values to request context before bedrock middleware
	var capturedUserID string
	var capturedAuthToken string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify context values are preserved
		if userID := r.Context().Value("user_id"); userID != nil {
			capturedUserID = userID.(string)
		}
		if token := r.Context().Value("auth_token"); token != nil {
			capturedAuthToken = token.(string)
		}
		w.WriteHeader(http.StatusOK)
	})

	// Wrap handler with middleware
	wrappedHandler := HTTPMiddleware(ctx, handler)

	// Execute: Send request with context values
	req := httptest.NewRequest("GET", "/test", nil)
	reqCtx := context.WithValue(req.Context(), "user_id", "user-123")
	reqCtx = context.WithValue(reqCtx, "auth_token", "token-abc")
	req = req.WithContext(reqCtx)

	rr := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rr, req)

	// Verify: user_id and auth_token are still accessible
	if capturedUserID != "user-123" {
		t.Errorf("expected user_id 'user-123', got %q", capturedUserID)
	}
	if capturedAuthToken != "token-abc" {
		t.Errorf("expected auth_token 'token-abc', got %q", capturedAuthToken)
	}
}

func TestHTTPMiddleware_AddsBedrock(t *testing.T) {
	// Setup: Create bedrock and base context
	ctx, close := Init(context.Background(),
		WithConfig(Config{ServiceName: "test-service"}),
	)
	defer close()

	var capturedBedrock *Bedrock
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBedrock = FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	wrappedHandler := HTTPMiddleware(ctx, handler)

	// Execute: Send request without bedrock in request context
	req := httptest.NewRequest("GET", "/test", nil)
	// Request context intentionally has no bedrock

	rr := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rr, req)

	// Verify: bedrock was added to request context
	if capturedBedrock == nil {
		t.Fatal("expected bedrock in request context")
	}
	if capturedBedrock.isNoop {
		t.Error("expected real bedrock, not noop")
	}
}

func TestHTTPMiddleware_MultipleContextValues(t *testing.T) {
	// Setup: Create bedrock
	ctx, close := Init(context.Background(),
		WithConfig(Config{ServiceName: "test-service"}),
	)
	defer close()

	// Test that bedrock + user_id + auth_token + request_id all coexist
	var capturedBedrock *Bedrock
	var capturedUserID string
	var capturedAuthToken string
	var capturedRequestID string

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBedrock = FromContext(r.Context())
		if userID := r.Context().Value("user_id"); userID != nil {
			capturedUserID = userID.(string)
		}
		if token := r.Context().Value("auth_token"); token != nil {
			capturedAuthToken = token.(string)
		}
		if reqID := r.Context().Value("request_id"); reqID != nil {
			capturedRequestID = reqID.(string)
		}
		w.WriteHeader(http.StatusOK)
	})

	// Wrap with bedrock middleware
	wrappedHandler := HTTPMiddleware(ctx, handler)

	// Execute: Send request with multiple context values
	req := httptest.NewRequest("GET", "/test", nil)
	reqCtx := context.WithValue(req.Context(), "user_id", "user-456")
	reqCtx = context.WithValue(reqCtx, "auth_token", "token-xyz")
	reqCtx = context.WithValue(reqCtx, "request_id", "req-789")
	req = req.WithContext(reqCtx)

	rr := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rr, req)

	// Verify all values are accessible
	if capturedBedrock == nil {
		t.Fatal("expected bedrock in context")
	}
	if capturedBedrock.isNoop {
		t.Error("expected real bedrock, not noop")
	}
	if capturedUserID != "user-456" {
		t.Errorf("expected user_id 'user-456', got %q", capturedUserID)
	}
	if capturedAuthToken != "token-xyz" {
		t.Errorf("expected auth_token 'token-xyz', got %q", capturedAuthToken)
	}
	if capturedRequestID != "req-789" {
		t.Errorf("expected request_id 'req-789', got %q", capturedRequestID)
	}
}

func TestHTTPMiddleware_OperationCreated(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{ServiceName: "test-service"}),
	)
	defer close()

	var opState *operationState
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		opState = operationStateFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	wrappedHandler := HTTPMiddleware(ctx, handler)

	req := httptest.NewRequest("GET", "/users", nil)
	rr := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rr, req)

	// Verify operation was created
	if opState == nil {
		t.Fatal("expected operation state in context")
	}
	if opState.name != "http.request" {
		t.Errorf("expected operation name 'http.request', got %q", opState.name)
	}

	// Verify HTTP attributes were added
	hasMethod := false
	hasPath := false
	opState.attrs.Range(func(a attr.Attr) bool {
		if a.Key == "http.method" && a.Value.AsString() == "GET" {
			hasMethod = true
		}
		if a.Key == "http.path" && a.Value.AsString() == "/users" {
			hasPath = true
		}
		return true
	})
	if !hasMethod {
		t.Error("expected http.method attribute")
	}
	if !hasPath {
		t.Error("expected http.path attribute")
	}
}

func TestHTTPMiddleware_CustomOperationName(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{ServiceName: "test-service"}),
	)
	defer close()

	var opState *operationState
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		opState = operationStateFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	wrappedHandler := HTTPMiddleware(ctx, handler,
		WithOperationName("custom.operation"),
	)

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rr, req)

	if opState == nil {
		t.Fatal("expected operation state")
	}
	if opState.name != "custom.operation" {
		t.Errorf("expected operation name 'custom.operation', got %q", opState.name)
	}
}

func TestHTTPMiddleware_StatusCodeCapture(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{ServiceName: "test-service"}),
	)
	defer close()

	var opState *operationState
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		opState = operationStateFromContext(r.Context())
		w.WriteHeader(http.StatusNotFound)
	})

	wrappedHandler := HTTPMiddleware(ctx, handler)

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rr, req)

	// Verify status code was captured
	hasStatusCode := false
	opState.attrs.Range(func(a attr.Attr) bool {
		if a.Key == "http.status_code" && a.Value.AsInt64() == 404 {
			hasStatusCode = true
			return false
		}
		return true
	})
	if !hasStatusCode {
		t.Error("expected http.status_code attribute with value 404")
	}

	// Verify operation was marked as failure (4xx)
	if opState.success {
		t.Error("expected operation to be marked as failure for 404 status")
	}
}

func TestHTTPMiddleware_AdditionalAttrs(t *testing.T) {
	ctx, close := Init(context.Background(),
		WithConfig(Config{ServiceName: "test-service"}),
	)
	defer close()

	var opState *operationState
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		opState = operationStateFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	wrappedHandler := HTTPMiddleware(ctx, handler,
		WithAdditionalAttrs(func(r *http.Request) []attr.Attr {
			return []attr.Attr{
				attr.String("custom.header", r.Header.Get("X-Custom")),
			}
		}),
	)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Custom", "custom-value")
	rr := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rr, req)

	// Verify custom attribute was added
	hasCustomAttr := false
	opState.attrs.Range(func(a attr.Attr) bool {
		if a.Key == "custom.header" && a.Value.AsString() == "custom-value" {
			hasCustomAttr = true
			return false
		}
		return true
	})
	if !hasCustomAttr {
		t.Error("expected custom.header attribute")
	}
}

func TestHTTPMiddleware_MiddlewareChain(t *testing.T) {
	// This test simulates a realistic middleware chain:
	// 1. Auth middleware (sets user_id)
	// 2. Request ID middleware (sets request_id)
	// 3. Bedrock middleware
	// 4. Handler

	ctx, close := Init(context.Background(),
		WithConfig(Config{ServiceName: "test-service"}),
	)
	defer close()

	var capturedUserID string
	var capturedRequestID string
	var capturedBedrock *Bedrock

	// Final handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if userID := r.Context().Value("user_id"); userID != nil {
			capturedUserID = userID.(string)
		}
		if reqID := r.Context().Value("request_id"); reqID != nil {
			capturedRequestID = reqID.(string)
		}
		capturedBedrock = FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	// Build middleware chain from innermost to outermost
	var wrappedHandler http.Handler = handler
	wrappedHandler = HTTPMiddleware(ctx, wrappedHandler) // 3. Bedrock

	// 2. Request ID middleware
	requestIDMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), "request_id", "req-12345")
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	wrappedHandler = requestIDMiddleware(wrappedHandler)

	// 1. Auth middleware
	authMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), "user_id", "user-abc")
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	wrappedHandler = authMiddleware(wrappedHandler)

	// Execute request through the full chain
	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(rr, req)

	// Verify all middleware values are preserved
	if capturedUserID != "user-abc" {
		t.Errorf("expected user_id 'user-abc', got %q", capturedUserID)
	}
	if capturedRequestID != "req-12345" {
		t.Errorf("expected request_id 'req-12345', got %q", capturedRequestID)
	}
	if capturedBedrock == nil {
		t.Fatal("expected bedrock in context")
	}
	if capturedBedrock.isNoop {
		t.Error("expected real bedrock, not noop")
	}
}
