package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func startupConfig(t *testing.T, mode string, options map[string]any) fixtureConfig {
	t.Helper()
	if options == nil {
		options = make(map[string]any)
	}
	options["Mode"] = mode
	raw, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	return fixtureConfig{Scenario: "startup", ScenarioConfig: raw}
}

func startupEnvironmentCall(t *testing.T, app *fixtureApp, name string, values map[string]string) (map[string]any, string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"Case": name, "Values": values})
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Post(app.URL+"/environment", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var result struct {
		Value map[string]any
		Error string
	}
	if response.StatusCode != 200 {
		t.Fatalf("environment response status %d", response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result.Value, result.Error
}

func TestStartupEnvironmentValues(t *testing.T) {
	app := startApp(t, startupConfig(t, "parse", nil))
	values := map[string]string{"STARTUP_STRING": "configured", "STARTUP_INT": "-32", "STARTUP_INT8": "-128", "STARTUP_INT16": "-32768", "STARTUP_INT32": "-2147483648", "STARTUP_INT64": "-9223372036854775808", "STARTUP_UINT": "32", "STARTUP_UINT8": "255", "STARTUP_UINT16": "65535", "STARTUP_UINT32": "4294967295", "STARTUP_UINT64": "18446744073709551615", "STARTUP_UINTPTR": "8", "STARTUP_FLOAT32": "1.5", "STARTUP_FLOAT64": "2.75", "STARTUP_BOOL": "true", "STARTUP_DURATION": "125ms", "STARTUP_LIST": "1, 2, -3", "STARTUP_MAP": "1=true, 2=false", "STARTUP_NESTED_VALUE": "nested-value", "STARTUP_PRIVATE": "private"}
	got, message := startupEnvironmentCall(t, app, "all", values)
	if message != "" {
		t.Fatal(message)
	}
	expected := map[string]any{"String": "configured", "Int": float64(-32), "Int8": float64(-128), "Int16": float64(-32768), "Int32": float64(-2147483648), "Int64": float64(-9223372036854775808), "Uint": float64(32), "Uint8": float64(255), "Uint16": float64(65535), "Uint32": float64(4294967295), "Uint64": float64(18446744073709551615), "Uintptr": float64(8), "Float32": 1.5, "Float64": 2.75, "Bool": true, "Duration": float64(125000000), "List": []any{float64(1), float64(2), float64(-3)}, "Map": map[string]any{"1": true, "2": false}, "Nested": map[string]any{"Value": "nested-value"}, "Ignored": map[string]any{"Value": ""}, "Untagged": ""}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("parsed values\ngot %#v\nwant %#v", got, expected)
	}
	raw, err := json.Marshal(map[string]any{"Case": "all", "Values": values})
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Post(app.URL+"/environment", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var exact struct {
		Value struct {
			Int64  int64
			Uint64 uint64
		}
		Error string
	}
	err = json.NewDecoder(response.Body).Decode(&exact)
	_ = response.Body.Close()
	if err != nil || exact.Error != "" || exact.Value.Int64 != -9223372036854775808 || exact.Value.Uint64 != 18446744073709551615 {
		t.Fatalf("exact integer boundaries: %+v, decode %v", exact, err)
	}
	got, message = startupEnvironmentCall(t, app, "all", map[string]string{"STARTUP_NESTED_VALUE": "present"})
	if message != "" || got["String"] != "default-name" || got["Int"] != float64(0) || got["List"] != nil || got["Map"] != nil {
		t.Fatalf("defaults: %#v, %s", got, message)
	}
	got, message = startupEnvironmentCall(t, app, "prefix", map[string]string{"STARTUP_PREFIX_VALUE": "prefixed"})
	if message != "" || got["Value"] != "prefixed" {
		t.Fatalf("prefix: %#v, %s", got, message)
	}
	for _, tc := range []struct {
		name, value string
		want        any
	}{{"required", "present", "present"}, {"not-empty", "present", "present"}, {"int8", "127", float64(127)}, {"uint8", "255", float64(255)}, {"float32", "0.5", 0.5}, {"bool", "false", false}, {"duration", "1s", float64(1000000000)}} {
		got, message = startupEnvironmentCall(t, app, tc.name, map[string]string{"STARTUP_VALUE": tc.value})
		if message != "" || !reflect.DeepEqual(got["Value"], tc.want) {
			t.Fatalf("%s value: %#v, %s", tc.name, got, message)
		}
	}
	for _, name := range []string{"from-valid", "from-nonstring"} {
		got, message = startupEnvironmentCall(t, app, name, nil)
		if message != "" {
			t.Fatalf("%s: %#v, %s", name, got, message)
		}
	}
}

func TestStartupEnvironmentRejectsInvalidConfiguration(t *testing.T) {
	app := startApp(t, startupConfig(t, "parse", nil))
	for _, tc := range []struct{ name, value, want string }{
		{"required", "", "required environment variable"}, {"not-empty", "", "must not be empty"}, {"int8", "128", "invalid int"}, {"uint8", "256", "invalid uint"}, {"uint8", "-1", "invalid uint"}, {"float32", "1e100", "invalid float"}, {"bool", "maybe", "invalid bool"}, {"duration", "soon", "invalid duration"}, {"slice", "1,no", "element 1"}, {"map", "malformed", "invalid map entry"}, {"map", "bad=true", "map key"}, {"map", "1=bad", "map value"}, {"unsupported", "1", "unsupported type"}, {"empty-tag", "", "env tag name must not be empty"}, {"unknown-option", "", "unknown env tag option"}, {"trailing-option", "", "unknown env tag option"}, {"skip-option", "", "cannot have options"}, {"nested-tag", "", "env tag name must not be empty"}, {"from-required", "", "required but not set"}, {"from-empty", "", "must not be empty"}, {"from-nested", "", "required but not set"}, {"from-invalid", "", "unknown env tag option"},
	} {
		t.Run(tc.name+"/"+tc.value, func(t *testing.T) {
			_, message := startupEnvironmentCall(t, app, tc.name, map[string]string{"STARTUP_VALUE": tc.value})
			if !strings.Contains(message, tc.want) {
				t.Fatalf("error %q lacks %q", message, tc.want)
			}
		})
	}
}

func TestStartupBedrockEnvironmentAndPrecedence(t *testing.T) {
	for _, mode := range []string{"fromenv", "mustenv", "implicit", "explicit"} {
		t.Run(mode, func(t *testing.T) {
			receiver := newReceiver(t)
			cfg := startupConfig(t, mode, map[string]any{"Service": "explicit-service"})
			cfg.TraceEndpoint = receiver.URL + "/v1/traces"
			cfg.Environment = map[string]string{"BEDROCK_SERVICE": "environment-service", "BEDROCK_TRACE_URL": cfg.TraceEndpoint, "BEDROCK_TRACE_PROTOCOL": "http", "BEDROCK_SERVER_ENABLED": "false", "BEDROCK_DISABLE_ENVIRONMENT_DETECTION": "true", "BEDROCK_RUNTIME_METRICS": "false", "BEDROCK_METRIC_BUCKETS": "0.1,0.5,1", "BEDROCK_SHUTDOWN_TIMEOUT": "2s"}
			app := startApp(t, cfg)
			getBehavior(t, app.URL+"/operation")
			app.Stop(t)
			request := nextBehaviorExport(t, receiver.Requests)
			if spans := behaviorSpans(t, request); len(spans) != 1 || spans[0].Name != "startup.operation" {
				t.Fatalf("startup spans: %v", spans)
			}
			expected := "environment-service"
			if mode == "explicit" {
				expected = "explicit-service"
			}
			resources := startupResource(t, request)
			if resources["service.name"] != expected {
				t.Fatalf("service resource: %#v", resources)
			}
		})
	}
}

func TestStartupInvalidBedrockConfigurationExits(t *testing.T) {
	for _, mode := range []string{"fromenv", "mustenv", "implicit"} {
		t.Run(mode, func(t *testing.T) {
			cfg := startupConfig(t, mode, nil)
			cfg.Environment = map[string]string{"BEDROCK_TRACE_SAMPLE_RATE": "bad", "BEDROCK_SERVER_ENABLED": "false", "BEDROCK_DISABLE_ENVIRONMENT_DETECTION": "true"}
			logs := startupFailure(t, cfg)
			if !strings.Contains(logs, "failed to parse config from env") || !strings.Contains(logs, "invalid float") {
				t.Fatalf("missing parse failure: %s", logs)
			}
		})
	}
}

func startupFailure(t *testing.T, cfg fixtureConfig) string {
	t.Helper()
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../../.."))
	binary := filepath.Join(t.TempDir(), "startup-failure")
	covered := os.Getenv("E2E_COVER_DIR") != ""
	if err := os.WriteFile(binary, fixtureBinary(t, root, covered), 0700); err != nil {
		t.Fatal(err)
	}
	coverDir := t.TempDir()
	if covered {
		base, err := filepath.Abs(os.Getenv("E2E_COVER_DIR"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(base, 0755); err != nil {
			t.Fatal(err)
		}
		coverDir, err = os.MkdirTemp(base, "fixture-failure-")
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary)
	cmd.Dir = root
	cmd.Env = append(cleanEnvironment(), "BEDROCK_E2E_CONFIG="+string(raw), "GOCOVERDIR="+coverDir)
	output, runErr := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("invalid startup did not exit")
	}
	if runErr == nil {
		t.Fatalf("invalid startup succeeded: %s", output)
	}
	if !strings.Contains(string(output), "invalid float") {
		t.Fatalf("unexpected startup failure: %v: %s", runErr, output)
	}
	return string(output)
}

func startupResource(t *testing.T, request exportRequest) map[string]any {
	t.Helper()
	var decoded struct {
		ResourceSpans []struct {
			Resource struct {
				Attributes []struct {
					Key   string
					Value map[string]any
				}
			}
		}
	}
	if err := json.Unmarshal(request.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	result := make(map[string]any)
	for _, resource := range decoded.ResourceSpans {
		for _, entry := range resource.Resource.Attributes {
			for _, value := range entry.Value {
				result[entry.Key] = value
			}
		}
	}
	return result
}

func TestStartupEnvironmentDetectionAndPrivacy(t *testing.T) {
	for _, provider := range []string{"aws", "gcp", "absent"} {
		for _, metadata := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/metadata=%t", provider, metadata), func(t *testing.T) {
				var calls atomic.Int64
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					switch {
					case provider == "aws" && r.URL.Path == "/latest/meta-data/instance-id":
						_, _ = w.Write([]byte("instance-visible"))
					case provider == "aws" && r.URL.Path == "/latest/meta-data/placement/region":
						_, _ = w.Write([]byte("region-visible"))
					case provider == "gcp" && strings.HasPrefix(r.URL.Path, "/computeMetadata/"):
						if r.Header.Get("Metadata-Flavor") != "Google" {
							t.Error("GCP metadata header missing")
						}
						if strings.HasSuffix(r.URL.Path, "/id") {
							_, _ = w.Write([]byte("gcp-instance-visible"))
						} else {
							_, _ = w.Write([]byte("projects/123/zones/us-central1-a"))
						}
					default:
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer proxy.Close()
				receiver := newReceiver(t)
				cfg := startupConfig(t, "detection", map[string]any{"Metadata": metadata, "Override": true})
				cfg.TraceEndpoint = receiver.URL + "/v1/traces"
				cfg.Environment = map[string]string{"HTTP_PROXY": proxy.URL, "HTTPS_PROXY": proxy.URL, "NO_PROXY": "127.0.0.1,localhost", "HOSTNAME": "pod-visible", "KUBERNETES_NAMESPACE": "namespace-visible", "KUBERNETES_NODE_NAME": "node-visible", "KUBERNETES_CONTAINER_NAME": "container-visible"}
				app := startApp(t, cfg)
				getBehavior(t, app.URL+"/operation")
				app.Stop(t)
				resources := startupResource(t, nextBehaviorExport(t, receiver.Requests))
				if calls.Load() == 0 {
					t.Fatal("metadata detection did not run")
				}
				if resources["service.version"] != "explicit-version" {
					t.Fatalf("explicit resource override: %#v", resources)
				}
				if metadata {
					for key, want := range map[string]string{"host.name": "explicit-host", "k8s.pod.name": "pod-visible", "k8s.namespace": "namespace-visible", "k8s.node.name": "node-visible", "k8s.container.name": "container-visible"} {
						if resources[key] != want {
							t.Fatalf("resource %s=%v, want %s", key, resources[key], want)
						}
					}
					if provider != "absent" && resources["cloud.provider"] != provider {
						t.Fatalf("cloud resource: %#v", resources)
					}
					if provider == "gcp" && resources["cloud.region"] != "us-central1" {
						t.Fatalf("GCP region: %#v", resources)
					}
				} else {
					for key := range resources {
						if strings.HasPrefix(key, "host.") || strings.HasPrefix(key, "process.") || strings.HasPrefix(key, "k8s.") {
							t.Fatalf("private resource escaped: %s=%v", key, resources[key])
						}
					}
				}
			})
		}
	}
}

func TestStartupDetectionWithoutDownwardAPI(t *testing.T) {
	proxy := httptest.NewServer(http.NotFoundHandler())
	defer proxy.Close()
	receiver := newReceiver(t)
	cfg := startupConfig(t, "detection", map[string]any{"Metadata": true, "Override": true})
	cfg.TraceEndpoint = receiver.URL + "/v1/traces"
	cfg.Environment = map[string]string{"HTTP_PROXY": proxy.URL, "HTTPS_PROXY": proxy.URL, "NO_PROXY": "127.0.0.1,localhost", "HOSTNAME": "", "KUBERNETES_NAMESPACE": "", "KUBERNETES_NODE_NAME": "", "KUBERNETES_CONTAINER_NAME": ""}
	app := startApp(t, cfg)
	getBehavior(t, app.URL+"/operation")
	app.Stop(t)
	resources := startupResource(t, nextBehaviorExport(t, receiver.Requests))
	if resources["host.name"] != "explicit-host" || resources["service.version"] != "explicit-version" || resources["process.executable.name"] != "bedrock-e2e" || resources["process.pid"] == nil {
		t.Fatalf("fallback detection resource: %#v", resources)
	}
	if _, exists := resources["k8s.node.name"]; exists {
		t.Fatal("missing downward API produced a node name")
	}
}

func TestStartupPublicObservabilityServer(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			app := startApp(t, startupConfig(t, "server", map[string]any{"Metrics": enabled}))
			var address struct{ URL string }
			startupGetJSON(t, app.ControlURL+"/server", &address)
			for _, path := range []string{"/health", "/ready"} {
				r, err := http.Get(address.URL + path)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(r.Body)
				_ = r.Body.Close()
				if r.StatusCode != 200 || string(body) != "ok" {
					t.Fatalf("%s: %d %q", path, r.StatusCode, body)
				}
			}
			for path, want := range map[string]int{"/debug/pprof/": 404, "/metrics": map[bool]int{true: 200, false: 404}[enabled]} {
				r, err := http.Get(address.URL + path)
				if err != nil {
					t.Fatal(err)
				}
				_ = r.Body.Close()
				if r.StatusCode != want {
					t.Fatalf("%s: %d want %d", path, r.StatusCode, want)
				}
			}
			app.Stop(t)
			if r, err := (&http.Client{Timeout: time.Second}).Get(address.URL + "/health"); err == nil {
				_ = r.Body.Close()
				t.Fatal("observability server survived shutdown")
			}
		})
	}
	app := startApp(t, startupConfig(t, "bind-failure", nil))
	var result struct{ Error string }
	startupGetJSON(t, app.ControlURL+"/bind", &result)
	if !strings.Contains(result.Error, "unknown port") {
		t.Fatalf("bind failure: %q", result.Error)
	}
}

func TestStartupAutomaticServerLifecycle(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	app := startApp(t, startupConfig(t, "explicit", map[string]any{"Automatic": true, "Address": address, "Metrics": true}))
	url := "http://" + address + "/health"
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, err := (&http.Client{Timeout: time.Second}).Get(url)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatalf("health status: %d", response.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	app.Stop(t)
	if response, err := (&http.Client{Timeout: time.Second}).Get(url); err == nil {
		_ = response.Body.Close()
		t.Fatal("automatic server survived cleanup")
	}
}

func startupGetJSON(t *testing.T, url string, target any) {
	t.Helper()
	response, err := (&http.Client{Timeout: 3 * time.Second}).Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		t.Fatalf("JSON response: %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
		t.Fatal(err)
	}
}
