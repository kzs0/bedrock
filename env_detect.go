package bedrock

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/kzs0/bedrock/attr"
)

// detectEnvironment runs all environment detectors and returns discovered
// attributes. Each detector is isolated: panics are recovered silently and
// HTTP calls are bounded by a 500ms timeout. Detected attributes have lower
// priority than user-supplied WithStaticAttrs values because they are prepended
// before user attrs (attr.NewSet last-wins deduplication handles the override).
func detectEnvironment() []attr.Attr {
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

	safeDetect(detectProcess)
	safeDetect(detectGit)
	safeDetect(detectKubernetes)
	safeDetect(detectContainer)
	safeDetect(detectCloud)

	return out
}

// detectProcess collects basic process information.
func detectProcess() []attr.Attr {
	var out []attr.Attr

	if pid := os.Getpid(); pid != 0 {
		out = append(out, attr.Int("process.pid", pid))
	}

	if exe, err := os.Executable(); err == nil && exe != "" {
		out = append(out, attr.String("process.executable.name", filepath.Base(exe)))
	}

	if host, err := os.Hostname(); err == nil && host != "" {
		out = append(out, attr.String("host.name", host))
	}

	return out
}

// detectGit reads the VCS revision from build info (populated by go build).
func detectGit() []attr.Attr {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			rev := s.Value
			if len(rev) > 12 {
				rev = rev[:12]
			}
			return []attr.Attr{attr.String("service.version", rev)}
		}
	}
	return nil
}

// detectKubernetes collects Kubernetes downward API attributes.
func detectKubernetes() []attr.Attr {
	var out []attr.Attr

	// Pod name: HOSTNAME env var or /etc/hostname file.
	if pod := os.Getenv("HOSTNAME"); pod != "" {
		out = append(out, attr.String("k8s.pod.name", pod))
	} else if data, err := os.ReadFile("/etc/hostname"); err == nil {
		if pod := strings.TrimSpace(string(data)); pod != "" {
			out = append(out, attr.String("k8s.pod.name", pod))
		}
	}

	// Namespace: env var or service account namespace file.
	if ns := os.Getenv("KUBERNETES_NAMESPACE"); ns != "" {
		out = append(out, attr.String("k8s.namespace", ns))
	} else if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := strings.TrimSpace(string(data)); ns != "" {
			out = append(out, attr.String("k8s.namespace", ns))
		}
	}

	// Node name and container name: only available via downward API env vars.
	if node := os.Getenv("KUBERNETES_NODE_NAME"); node != "" {
		out = append(out, attr.String("k8s.node.name", node))
	}
	if container := os.Getenv("KUBERNETES_CONTAINER_NAME"); container != "" {
		out = append(out, attr.String("k8s.container.name", container))
	}

	return out
}

// detectContainer attempts to read the container ID from /proc/self/cgroup.
// This is skipped when running in Kubernetes (KUBERNETES_NODE_NAME is set).
func detectContainer() []attr.Attr {
	// Don't double-detect: if we're in k8s, skip Docker container ID.
	if os.Getenv("KUBERNETES_NODE_NAME") != "" {
		return nil
	}

	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		// Try /proc/self/mountinfo as fallback.
		data, err = os.ReadFile("/proc/self/mountinfo")
		if err != nil {
			return nil
		}
	}

	for _, line := range strings.Split(string(data), "\n") {
		id := extractContainerID(line)
		if id != "" {
			return []attr.Attr{attr.String("container.id", id)}
		}
	}
	return nil
}

// extractContainerID parses a 64-char hex container ID from a cgroup/mountinfo
// line. It handles formats like:
//   - /docker/{id}
//   - /docker-{id}.scope
//   - /containerd-{id}.scope
func extractContainerID(line string) string {
	parts := strings.Split(line, "/")
	// Known runtime prefixes that may precede the container ID.
	prefixes := []string{"docker-", "containerd-", "cri-containerd-"}
	for i := len(parts) - 1; i >= 0; i-- {
		seg := parts[i]

		// Strip suffix starting at the last dot (e.g., ".scope").
		if idx := strings.LastIndexByte(seg, '.'); idx > 0 {
			seg = seg[:idx]
		}

		// Try stripping known runtime prefixes.
		for _, pfx := range prefixes {
			if strings.HasPrefix(seg, pfx) {
				seg = seg[len(pfx):]
				break
			}
		}

		if len(seg) == 64 && isHexString(seg) {
			return seg
		}
	}
	return ""
}

func isHexString(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// detectCloud tries AWS IMDS first, then GCP metadata server. Only one is
// expected to succeed.
func detectCloud() []attr.Attr {
	if attrs := detectAWS(); len(attrs) > 0 {
		return attrs
	}
	return detectGCP()
}

// detectAWS and detectGCP are variables so tests can override them with mock
// server URLs without patching global state.
var detectAWS = func() []attr.Attr {
	return detectAWSWithBase("http://169.254.169.254")
}

var detectGCP = func() []attr.Attr {
	return detectGCPWithBase("http://metadata.google.internal")
}

// detectAWSWithBase queries the AWS Instance Metadata Service (IMDSv1) at the
// given base URL with a 500ms timeout.
func detectAWSWithBase(base string) []attr.Attr {
	client := &http.Client{Timeout: 500 * time.Millisecond}

	instanceID := imdsGet(client, base+"/latest/meta-data/instance-id", nil)
	if instanceID == "" {
		return nil
	}

	region := imdsGet(client, base+"/latest/meta-data/placement/region", nil)

	out := []attr.Attr{
		attr.String("cloud.provider", "aws"),
		attr.String("host.id", instanceID),
	}
	if region != "" {
		out = append(out, attr.String("cloud.region", region))
	}
	return out
}

// detectGCPWithBase queries the GCP Compute Engine metadata server at the
// given base URL with a 500ms timeout.
func detectGCPWithBase(base string) []attr.Attr {
	client := &http.Client{Timeout: 500 * time.Millisecond}

	headers := map[string]string{"Metadata-Flavor": "Google"}
	instanceID := imdsGet(client, base+"/computeMetadata/v1/instance/id", headers)
	if instanceID == "" {
		return nil
	}

	zone := imdsGet(client, base+"/computeMetadata/v1/instance/zone", headers)
	// GCP zone looks like "projects/123/zones/us-central1-a"; extract last segment.
	region := ""
	if zone != "" {
		parts := strings.Split(zone, "/")
		z := parts[len(parts)-1]
		// Strip trailing zone letter to get region (e.g. "us-central1-a" → "us-central1").
		if idx := strings.LastIndexByte(z, '-'); idx > 0 {
			region = z[:idx]
		}
	}

	out := []attr.Attr{
		attr.String("cloud.provider", "gcp"),
		attr.String("host.id", instanceID),
	}
	if region != "" {
		out = append(out, attr.String("cloud.region", region))
	}
	return out
}

// imdsGet performs a GET request against a metadata endpoint with optional
// headers. Returns the response body trimmed of whitespace, or "" on any error.
func imdsGet(client *http.Client, url string, headers map[string]string) string {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}
