package e2e

import (
	"bufio"
	"bytes"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type fixtureConfig struct {
	TraceEndpoint           string
	TraceProtocol           string
	TraceInsecure           bool
	ProfileCAFile           string
	ProfileHeaders          map[string]string
	CloudEndpoint           string
	CloudAPIKey             string
	CloudProfileTypes       []string
	CloudPushIntervalMillis int
	ProfileEndpoint         string
	ProfileTypes            []string
	SampleRate              *float64
	ErrorDetails            bool
	ProfileLabels           bool
	MetricFamilyLimit       int
	ProfileTimeoutMillis    int
	ProfileRetries          int
	ProfileMaxBytes         int
	ShutdownMillis          int
	MetricSeriesLimit       int
	CPUWindowMillis         int
	ProfileIntervalMillis   int
	DownstreamURL           string
	ContentionSampling      bool
}

type lockedBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}
func (b *lockedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.data.String() }

type fixtureApp struct {
	URL          string
	ControlURL   string
	MetricsURL   string
	logs         lockedBuffer
	cmd          *exec.Cmd
	done         chan struct{}
	waitErr      error
	stop         sync.Once
	stopErr      error
	stopObserved bool
}

func (a *fixtureApp) Logs() string { return a.logs.String() }

type fixtureBuildMode struct {
	covered bool
	raced   bool
}

var fixtureBuilds = struct {
	sync.Mutex
	binaries map[fixtureBuildMode][]byte
}{binaries: make(map[fixtureBuildMode][]byte)}

func fixtureBinary(t *testing.T, root string, covered bool) []byte {
	t.Helper()
	mode := fixtureBuildMode{covered: covered, raced: os.Getenv("E2E_RACE") == "1"}
	fixtureBuilds.Lock()
	defer fixtureBuilds.Unlock()
	if binary, ok := fixtureBuilds.binaries[mode]; ok {
		return binary
	}
	outputPath := filepath.Join(t.TempDir(), "fixture-build")
	args := []string{"build", "-o", outputPath}
	if mode.raced {
		args = append(args, "-race")
	}
	buildCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	//nolint:staticcheck // Use this test's compiler.
	compiler := filepath.Join(runtime.GOROOT(), "bin", "go")
	if covered {
		list := exec.CommandContext(buildCtx, compiler, "list", "./...")
		list.Dir = root
		list.Env = cleanEnvironment()
		output, err := list.Output()
		if err != nil {
			t.Fatalf("list coverage packages: %v", err)
		}
		var packages []string
		for _, pkg := range strings.Fields(string(output)) {
			if pkg == "github.com/kzs0/bedrock/example" || strings.HasPrefix(pkg, "github.com/kzs0/bedrock/example/") {
				continue
			}
			packages = append(packages, pkg)
		}
		packages = append(packages, "github.com/kzs0/bedrock/testdata/e2eapp")
		args = append(args, "-cover", "-coverpkg="+strings.Join(packages, ","))
		if mode.raced {
			args = append(args, "-covermode=atomic")
		}
	}
	args = append(args, "./testdata/e2eapp")
	build := exec.CommandContext(buildCtx, compiler, args...)
	build.Dir = root
	build.Env = cleanEnvironment()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, output)
	}
	if mode.raced {
		info, err := buildinfo.ReadFile(outputPath)
		if err != nil {
			t.Fatalf("read fixture build settings: %v", err)
		}
		raced := false
		for _, setting := range info.Settings {
			if setting.Key == "-race" && setting.Value == "true" {
				raced = true
			}
		}
		if !raced {
			t.Fatal("fixture lacks race instrumentation")
		}
	}
	binary, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	fixtureBuilds.binaries[mode] = binary
	return binary
}

func startApp(t *testing.T, cfg fixtureConfig) *fixtureApp {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate fixture root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../../.."))
	binary := filepath.Join(t.TempDir(), "bedrock-e2e")
	coverDir := os.Getenv("E2E_COVER_DIR")
	covered := coverDir != ""
	if coverDir != "" {
		if err := os.MkdirAll(coverDir, 0o755); err != nil {
			t.Fatal(err)
		}
		var err error
		coverDir, err = filepath.Abs(coverDir)
		if err != nil {
			t.Fatal(err)
		}
		coverDir, err = os.MkdirTemp(coverDir, "fixture-")
		if err != nil {
			t.Fatal(err)
		}
	} else {
		coverDir = t.TempDir()
	}
	if err := os.WriteFile(binary, fixtureBinary(t, root, covered), 0o700); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := &fixtureApp{done: make(chan struct{})}
	a.cmd = exec.Command(binary)
	a.cmd.Dir = root
	a.cmd.Env = append(cleanEnvironment(), "BEDROCK_E2E_CONFIG="+string(config), "GOCOVERDIR="+coverDir)
	a.cmd.Stderr = &a.logs
	stdout, err := a.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = a.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { a.waitErr = a.cmd.Wait(); close(a.done) }()
	t.Cleanup(func() {
		a.Stop(t)
		if t.Failed() {
			t.Logf("fixture logs:\n%s", a.Logs())
		}
	})
	type readyResult struct {
		URLs map[string]string
		Err  error
	}
	ready := make(chan readyResult, 1)
	go func() {
		var urls map[string]string
		reader := bufio.NewReaderSize(stdout, 4096)
		line, readErr := reader.ReadBytes('\n')
		if readErr == nil {
			readErr = json.Unmarshal(line, &urls)
		}
		ready <- readyResult{urls, readErr}
		_, _ = io.Copy(io.Discard, reader)
	}()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case result := <-ready:
		if result.Err != nil {
			t.Fatalf("fixture readiness: %v\n%s", result.Err, a.Logs())
		}
		a.URL, a.ControlURL, a.MetricsURL = result.URLs["app_url"], result.URLs["control_url"], result.URLs["metrics_url"]
		if a.URL == "" || a.ControlURL == "" || a.MetricsURL == "" {
			t.Fatalf("incomplete readiness: %v", result.URLs)
		}
	case <-a.done:
		t.Fatalf("fixture exited before readiness: %v\n%s", a.waitErr, a.Logs())
	case <-timer.C:
		t.Fatalf("fixture readiness timed out\n%s", a.Logs())
	}
	return a
}

func cleanEnvironment() []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "BEDROCK_") || strings.HasPrefix(key, "OTEL_") || key == "GOCOVERDIR" {
			continue
		}
		env = append(env, entry)
	}
	return env
}

func (a *fixtureApp) Stop(t *testing.T) {
	t.Helper()
	if a.stopObserved {
		return
	}
	if err := a.StopError(t); err != nil {
		t.Errorf("fixture exit: %v\n%s", err, a.Logs())
	}
}

func (a *fixtureApp) Signal(t *testing.T) {
	t.Helper()
	if err := a.SignalError(t); err != nil {
		t.Errorf("fixture signal exit: %v\n%s", err, a.Logs())
	}
}

func (a *fixtureApp) SignalError(t *testing.T) error {
	t.Helper()
	return a.stopWith(t, true)
}

func (a *fixtureApp) StopError(t *testing.T) error {
	t.Helper()
	return a.stopWith(t, false)
}

func (a *fixtureApp) stopWith(t *testing.T, signal bool) error {
	t.Helper()
	a.stopObserved = true
	a.stop.Do(func() {
		select {
		case <-a.done:
		default:
			if signal {
				_ = a.cmd.Process.Signal(syscall.SIGTERM)
			} else if a.ControlURL != "" {
				client := &http.Client{Timeout: time.Second}
				resp, err := client.Post(a.ControlURL+"/shutdown", "", nil)
				if err == nil {
					_ = resp.Body.Close()
				}
			} else {
				_ = a.cmd.Process.Signal(os.Interrupt)
			}
			timer := time.NewTimer(8 * time.Second)
			defer timer.Stop()
			select {
			case <-a.done:
			case <-timer.C:
				_ = a.cmd.Process.Kill()
				<-a.done
				a.stopErr = fmt.Errorf("fixture shutdown timed out; forced kill")
			}
		}
		if a.stopErr == nil {
			a.stopErr = a.waitErr
		}
	})
	return a.stopErr
}

type fixtureStats struct {
	Trace    struct{ DroppedSpans, FailedExports uint64 } `json:"trace"`
	Profiles struct{ Exported, Failed, Skipped uint64 }   `json:"profiles"`
	Metrics  struct{ DroppedSeries uint64 }               `json:"metrics"`
}

func (a *fixtureApp) FinalStats(t *testing.T) fixtureStats {
	t.Helper()
	select {
	case <-a.done:
	default:
		t.Fatal("final stats require fixture shutdown")
	}
	for _, line := range strings.Split(a.Logs(), "\n") {
		var record struct {
			Stats *fixtureStats `json:"fixture_final_stats"`
		}
		if json.Unmarshal([]byte(line), &record) == nil && record.Stats != nil {
			return *record.Stats
		}
	}
	t.Fatal("fixture final stats missing")
	return fixtureStats{}
}

type exportRequest struct {
	Path   string
	Header http.Header
	Body   []byte
}

type receiver struct {
	URL      string
	Requests <-chan exportRequest
	mu       sync.RWMutex
	handler  func(http.ResponseWriter, *http.Request)
	dropped  atomic.Uint64
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	requests := make(chan exportRequest, 256)
	r := &receiver{Requests: requests}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(io.LimitReader(req.Body, (32<<20)+1))
		if err != nil {
			http.Error(w, "read export", http.StatusBadRequest)
			return
		}
		if len(body) > 32<<20 {
			http.Error(w, "export too large", http.StatusRequestEntityTooLarge)
			return
		}
		export := exportRequest{Path: req.URL.Path, Header: req.Header.Clone(), Body: body}
		select {
		case requests <- export:
		default:
			r.dropped.Add(1)
		}
		r.mu.RLock()
		handler := r.handler
		r.mu.RUnlock()
		if handler != nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
			handler(w, req)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	r.URL = server.URL
	t.Cleanup(func() {
		server.Close()
		if n := r.dropped.Load(); n > 0 {
			t.Logf("receiver dropped %d captures", n)
		}
	})
	return r
}

func (r *receiver) SetHandler(handler func(http.ResponseWriter, *http.Request)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handler = handler
}

func TestFixtureControlBoundaries(t *testing.T) {
	app := startApp(t, fixtureConfig{ShutdownMillis: 500})
	client := &http.Client{Timeout: 2 * time.Second}
	for _, tc := range []struct {
		query string
		want  int
		count int
	}{{"n=", 400, 0}, {"n=-1", 400, 0}, {"n=10001", 400, 0}, {"n=invalid", 400, 0}, {"n=0", 200, 0}, {"n=3", 200, 3}, {"n=10000", 200, 10000}, {"n=1&payload_bytes=-1", 400, 0}, {"n=1&payload_bytes=4097", 400, 0}, {"n=1&payload_bytes=invalid", 400, 0}, {"n=1&payload_bytes=4096", 200, 1}} {
		response, err := client.Get(app.ControlURL + "/operations?" + tc.query)
		if err != nil {
			t.Fatal(err)
		}
		var result struct{ Count int }
		if response.StatusCode == http.StatusOK {
			err = json.NewDecoder(response.Body).Decode(&result)
		}
		_ = response.Body.Close()
		if response.StatusCode != tc.want || err != nil {
			t.Fatalf("operations %q: status %d, decode %v", tc.query, response.StatusCode, err)
		}
		if tc.want == http.StatusOK && result.Count != tc.count {
			t.Fatalf("operations %q returned %d", tc.query, result.Count)
		}
	}
	for _, tc := range []struct {
		path string
		want int
	}{{"start", 204}, {"start", 409}, {"stop", 204}, {"stop", 204}, {"start", 204}} {
		response, err := client.Post(app.ControlURL+"/cpu-profile/"+tc.path, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != tc.want {
			t.Fatalf("CPU %s status %d, want %d", tc.path, response.StatusCode, tc.want)
		}
	}
	app.Signal(t)
	stats := app.FinalStats(t)
	if stats.Trace.DroppedSpans != 0 || stats.Trace.FailedExports != 0 || stats.Profiles.Exported != 0 || stats.Profiles.Failed != 0 || stats.Profiles.Skipped != 0 {
		t.Fatalf("unexpected disabled exporter statistics: %+v", stats)
	}
}
