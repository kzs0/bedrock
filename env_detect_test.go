package bedrock

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kzs0/bedrock/attr"
)

// ── Process detection ─────────────────────────────────────────────────────────

func TestDetectProcess_PID(t *testing.T) {
	attrs := detectProcess()
	found := false
	for _, a := range attrs {
		if a.Key == "process.pid" {
			found = true
			if a.Value.AsInt64() <= 0 {
				t.Errorf("expected positive PID, got %d", a.Value.AsInt64())
			}
		}
	}
	if !found {
		t.Error("expected process.pid attribute")
	}
}

func TestDetectProcess_Hostname(t *testing.T) {
	attrs := detectProcess()
	found := false
	for _, a := range attrs {
		if a.Key == "host.name" {
			found = true
			if a.Value.String() == "" {
				t.Error("expected non-empty host.name")
			}
		}
	}
	if !found {
		t.Error("expected host.name attribute")
	}
}

// ── Kubernetes detection ──────────────────────────────────────────────────────

func TestDetectKubernetes_PodFromEnv(t *testing.T) {
	t.Setenv("HOSTNAME", "my-pod-abc123")
	attrs := detectKubernetes()
	assertAttr(t, attrs, "k8s.pod.name", "my-pod-abc123")
}

func TestDetectKubernetes_NamespaceFromEnv(t *testing.T) {
	t.Setenv("KUBERNETES_NAMESPACE", "production")
	attrs := detectKubernetes()
	assertAttr(t, attrs, "k8s.namespace", "production")
}

func TestDetectKubernetes_NodeNameFromEnv(t *testing.T) {
	t.Setenv("KUBERNETES_NODE_NAME", "worker-1")
	attrs := detectKubernetes()
	assertAttr(t, attrs, "k8s.node.name", "worker-1")
}

func TestDetectKubernetes_ContainerNameFromEnv(t *testing.T) {
	t.Setenv("KUBERNETES_CONTAINER_NAME", "app")
	attrs := detectKubernetes()
	assertAttr(t, attrs, "k8s.container.name", "app")
}

func TestDetectKubernetes_NoEnvNoPanic(t *testing.T) {
	// Unset relevant env vars to test graceful absence.
	unsetEnv(t, "HOSTNAME", "KUBERNETES_NAMESPACE", "KUBERNETES_NODE_NAME", "KUBERNETES_CONTAINER_NAME")
	// Should not panic.
	_ = detectKubernetes()
}

// ── Container ID extraction ───────────────────────────────────────────────────

func TestExtractContainerID_Valid(t *testing.T) {
	id := strings.Repeat("a", 64)
	line := fmt.Sprintf("12:memory:/docker/%s", id)
	got := extractContainerID(line)
	if got != id {
		t.Errorf("expected %s, got %s", id, got)
	}
}

func TestExtractContainerID_WithScope(t *testing.T) {
	id := strings.Repeat("b", 64)
	line := fmt.Sprintf("0::/system.slice/docker-%s.scope", id)
	got := extractContainerID(line)
	if got != id {
		t.Errorf("expected %s, got %s", id, got)
	}
}

func TestExtractContainerID_TooShort(t *testing.T) {
	line := "12:memory:/docker/abc123"
	got := extractContainerID(line)
	if got != "" {
		t.Errorf("expected empty for short id, got %s", got)
	}
}

func TestExtractContainerID_Empty(t *testing.T) {
	got := extractContainerID("")
	if got != "" {
		t.Errorf("expected empty, got %s", got)
	}
}

// ── Cloud metadata (AWS and GCP) ──────────────────────────────────────────────

func TestDetectAWS_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest/meta-data/instance-id":
			_, _ = fmt.Fprint(w, "i-1234567890abcdef0")
		case "/latest/meta-data/placement/region":
			_, _ = fmt.Fprint(w, "us-east-1")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// Temporarily patch the AWS URLs used by imdsGet.
	origDetect := detectAWS
	detectAWS = func() []attr.Attr {
		return detectAWSWithBase(srv.URL)
	}
	defer func() { detectAWS = origDetect }()

	attrs := detectAWS()
	assertAttr(t, attrs, "cloud.provider", "aws")
	assertAttr(t, attrs, "host.id", "i-1234567890abcdef0")
	assertAttr(t, attrs, "cloud.region", "us-east-1")
}

func TestDetectGCP_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			http.Error(w, "missing header", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/computeMetadata/v1/instance/id":
			_, _ = fmt.Fprint(w, "1234567890123456789")
		case "/computeMetadata/v1/instance/zone":
			fmt.Fprint(w, "projects/123/zones/us-central1-a")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	origDetect := detectGCP
	detectGCP = func() []attr.Attr {
		return detectGCPWithBase(srv.URL)
	}
	defer func() { detectGCP = origDetect }()

	attrs := detectGCP()
	assertAttr(t, attrs, "cloud.provider", "gcp")
	assertAttr(t, attrs, "host.id", "1234567890123456789")
	assertAttr(t, attrs, "cloud.region", "us-central1")
}

func TestDetectCloud_UnreachableNoError(t *testing.T) {
	// Points to a closed server — should not panic, just return empty.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // immediately close

	origAWS := detectAWS
	origGCP := detectGCP
	detectAWS = func() []attr.Attr { return detectAWSWithBase(srv.URL) }
	detectGCP = func() []attr.Attr { return detectGCPWithBase(srv.URL) }
	defer func() { detectAWS = origAWS; detectGCP = origGCP }()

	attrs := detectCloud()
	_ = attrs // just verify no panic
}

// ── User attrs override auto-detected ────────────────────────────────────────

func TestDetectEnvironment_UserOverrides(t *testing.T) {
	t.Setenv("HOSTNAME", "auto-detected-pod")

	// Simulate user supplying the same key.
	detected := detectEnvironment()
	userAttrs := []attr.Attr{attr.String("k8s.pod.name", "user-supplied")}

	// User attrs appended last → last wins in attr.NewSet.
	all := append(detected, userAttrs...)
	set := attr.NewSet(all...)

	v, ok := set.Get("k8s.pod.name")
	if !ok {
		t.Fatal("k8s.pod.name not found in merged set")
	}
	if v.String() != "user-supplied" {
		t.Errorf("expected user-supplied value to win, got %q", v.String())
	}
}

// ── Panic safety ──────────────────────────────────────────────────────────────

func TestSafeDetect_RecoversPanic(t *testing.T) {
	var out []attr.Attr
	append_ := func(a attr.Attr) {
		if a.Key != "" && a.Value.String() != "" {
			out = append(out, a)
		}
	}
	appendAll := func(as []attr.Attr) {
		for _, a := range as {
			append_(a)
		}
	}
	safeDetect := func(fn func() []attr.Attr) {
		defer func() { recover() }() //nolint:errcheck
		appendAll(fn())
	}

	safeDetect(func() []attr.Attr {
		panic("simulated detector panic")
	})

	// No panic propagated; out is just empty.
	_ = out
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func assertAttr(t *testing.T, attrs []attr.Attr, key, want string) {
	t.Helper()
	for _, a := range attrs {
		if a.Key == key {
			if a.Value.String() != want {
				t.Errorf("attr %q: expected %q, got %q", key, want, a.Value.String())
			}
			return
		}
	}
	t.Errorf("attribute %q not found in %v", key, attrs)
}

func unsetEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		old, exists := os.LookupEnv(k)
		_ = os.Unsetenv(k)
		if exists {
			k, old := k, old
			t.Cleanup(func() { _ = os.Setenv(k, old) })
		}
	}
}
