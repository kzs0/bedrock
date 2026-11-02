package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"sync"
	"syscall"
	"time"

	"github.com/kzs0/bedrock"
	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/metric/prometheus"
	"github.com/kzs0/bedrock/privacy"
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
	MetricSeriesLimit       int
	CPUWindowMillis         int
	ProfileIntervalMillis   int
	DownstreamURL           string
	ContentionSampling      bool
}

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	var cfg fixtureConfig
	if err := json.Unmarshal([]byte(os.Getenv("BEDROCK_E2E_CONFIG")), &cfg); err != nil {
		return err
	}
	bc := bedrock.DefaultConfig()
	bc.Service = "bedrock-e2e"
	bc.DisableEnvironmentDetection = true
	bc.ServerEnabled = false
	bc.TraceURL, bc.TraceProtocol, bc.TraceInsecure = cfg.TraceEndpoint, cfg.TraceProtocol, cfg.TraceInsecure
	bc.LogCanonical = true
	bc.DataPolicy = &privacy.Policy{AllowErrorDetails: cfg.ErrorDetails, AllowProfileLabels: cfg.ProfileLabels}
	if cfg.SampleRate != nil {
		bc.TraceSampleRate = *cfg.SampleRate
	}
	if cfg.MetricSeriesLimit > 0 {
		bc.MetricSeriesLimit = cfg.MetricSeriesLimit
	}
	if cfg.ProfileEndpoint != "" {
		interval := time.Duration(cfg.ProfileIntervalMillis) * time.Millisecond
		if interval == 0 {
			interval = 500 * time.Millisecond
		}
		window := time.Duration(cfg.CPUWindowMillis) * time.Millisecond
		if window == 0 {
			window = 100 * time.Millisecond
		}
		types := make([]bedrock.ProfileType, 0, len(cfg.ProfileTypes))
		for _, typ := range cfg.ProfileTypes {
			types = append(types, bedrock.ProfileType(typ))
		}
		bc.Profiles = &bedrock.ProfileExportConfig{Endpoint: cfg.ProfileEndpoint, AllowHTTP: true, Types: types, Interval: interval, CPUSampleDuration: window, Timeout: 2 * time.Second}
		bc.Profiles.Headers = cfg.ProfileHeaders
		if cfg.ProfileCAFile != "" {
			pem, readErr := os.ReadFile(cfg.ProfileCAFile)
			if readErr != nil {
				return readErr
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return errors.New("invalid profile CA")
			}
			bc.Profiles.TLSConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		}
		if cfg.ContentionSampling {
			bc.Profiles.MutexSampleFraction = 1
			bc.Profiles.BlockSampleRate = time.Nanosecond
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	static := []attr.Attr{attr.String("service.namespace", "visible-resource-marker"), attr.String("host.name", "secret-host-marker")}
	var b *bedrock.Bedrock
	var cleanup func()
	var err error
	if cfg.CloudEndpoint != "" {
		interval := time.Duration(cfg.ProfileIntervalMillis) * time.Millisecond
		if interval == 0 {
			interval = 500 * time.Millisecond
		}
		window := time.Duration(cfg.CPUWindowMillis) * time.Millisecond
		if window == 0 {
			window = 100 * time.Millisecond
		}
		push := time.Duration(cfg.CloudPushIntervalMillis) * time.Millisecond
		if push == 0 {
			push = 500 * time.Millisecond
		}
		types := make([]bedrock.ProfileType, 0, len(cfg.CloudProfileTypes))
		for _, typ := range cfg.CloudProfileTypes {
			types = append(types, bedrock.ProfileType(typ))
		}
		bc.ShutdownTimeout = 5 * time.Second
		ctx, cleanup = bedrock.Init(ctx, bedrock.WithConfig(bc), bedrock.WithStaticAttrs(static...), bedrock.WithCloud(cfg.CloudAPIKey, bedrock.CloudEndpoint(cfg.CloudEndpoint), bedrock.CloudInsecure(), bedrock.CloudPushInterval(push), bedrock.CloudProfileInterval(interval), bedrock.CloudProfileCPUSampleDuration(window), bedrock.CloudProfiles(types...)))
		b = bedrock.FromContext(ctx)
	} else {
		b, err = bedrock.New(bc, static...)
		if err != nil {
			return err
		}
		ctx = bedrock.WithBedrock(ctx, b)
	}
	stop := make(chan struct{})
	var stopOnce, workOnce sync.Once
	var work sync.WaitGroup
	requestStop := func() { stopOnce.Do(func() { close(stop) }) }
	mux := http.NewServeMux()
	mux.Handle("/", http.DefaultServeMux)
	mux.HandleFunc("GET /ok/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/fail", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	mux.HandleFunc("/hints", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/panic", func(http.ResponseWriter, *http.Request) { panic("fixture panic") })
	downstream := func(w http.ResponseWriter, r *http.Request) {
		if cfg.DownstreamURL == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		req, reqErr := http.NewRequestWithContext(r.Context(), http.MethodGet, cfg.DownstreamURL, nil)
		if reqErr != nil {
			http.Error(w, reqErr.Error(), http.StatusBadGateway)
			return
		}
		client := bedrock.NewClient(&http.Client{Timeout: 2 * time.Second})
		resp, callErr := client.Do(req)
		if callErr != nil {
			http.Error(w, callErr.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		w.WriteHeader(resp.StatusCode)
	}
	mux.HandleFunc("/downstream", downstream)
	mux.HandleFunc("/notrace", func(w http.ResponseWriter, r *http.Request) {
		op, opctx := bedrock.Operation(r.Context(), "fixture.notrace", bedrock.NoTrace())
		defer op.Done()
		downstream(w, r.WithContext(opctx))
	})
	mux.HandleFunc("/secret", func(w http.ResponseWriter, r *http.Request) {
		op, opctx := bedrock.Operation(r.Context(), "fixture.secret")
		defer op.Done()
		attrs := []attr.Attr{attr.String("password", "secret-password-marker"), attr.String("authorization", "secret-authorization-marker"), attr.Any("nested", map[string]any{"password": "secret-nested-marker", "safe": "visible-marker"}), attr.Error(errors.New("secret-error-marker")), attr.String("safe", "visible-marker")}
		op.Register(opctx, attrs...)
		op.Event(opctx, attr.NewEvent("fixture.secret.event", attrs...))
		bedrock.Info(opctx, "fixture.secret.log", attrs...)
		downstream(w, r.WithContext(opctx))
	})
	control := http.NewServeMux()
	control.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	control.HandleFunc("/stats", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"trace": b.TraceExportStats(), "profiles": b.ProfileStats(), "metrics": map[string]uint64{"DroppedSeries": b.Metrics().DroppedSeries()}})
	})
	control.HandleFunc("/shutdown", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent); requestStop() })
	control.HandleFunc("/workload", func(w http.ResponseWriter, _ *http.Request) {
		workOnce.Do(func() { startWorkload(ctx, &work) })
		w.WriteHeader(http.StatusNoContent)
	})
	metrics := http.NewServeMux()
	metrics.Handle("/metrics", prometheus.Handler(b.Metrics()))
	servers := []*http.Server{{Handler: bedrock.HTTPMiddleware(ctx, mux), ReadHeaderTimeout: time.Second}, {Handler: control, ReadHeaderTimeout: time.Second}, {Handler: metrics, ReadHeaderTimeout: time.Second}}
	urls := make([]string, 0, len(servers))
	for _, srv := range servers {
		listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
		if listenErr != nil {
			return listenErr
		}
		urls = append(urls, "http://"+listener.Addr().String())
		go func() {
			if serveErr := srv.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				log.Print(serveErr)
				requestStop()
			}
		}()
	}
	if err = json.NewEncoder(os.Stdout).Encode(map[string]string{"app_url": urls[0], "control_url": urls[1], "metrics_url": urls[2]}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
	case <-stop:
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	for _, srv := range servers {
		_ = srv.Shutdown(shutdownCtx)
	}
	work.Wait()
	if cleanup != nil {
		cleanup()
		return nil
	}
	return b.Shutdown(shutdownCtx)
}

var retainedHeap [][]byte

func startWorkload(ctx context.Context, wg *sync.WaitGroup) {
	for i := 0; i < 16; i++ {
		block := make([]byte, 1<<20)
		for j := range block {
			block[j] = byte(i + j)
		}
		retainedHeap = append(retainedHeap, block)
	}
	runtime.GC()
	launch := func(fn func()) {
		ready := make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			pprof.Do(ctx, pprof.Labels("workload", "fixture-workload-marker", "password", "secret-profile-marker"), func(context.Context) {
				close(ready)
				fn()
			})
		}()
		<-ready
	}
	for i := 0; i < 8; i++ {
		launch(func() { fixtureGoroutine(ctx) })
	}
	launch(func() { fixtureCPU(ctx) })
	var mutex sync.Mutex
	for i := 0; i < 4; i++ {
		launch(func() { fixtureContention(ctx, &mutex) })
	}
}

func fixtureGoroutine(ctx context.Context) { <-ctx.Done() }

func fixtureCPU(ctx context.Context) {
	var n uint64 = 1
	for {
		select {
		case <-ctx.Done():
			runtime.KeepAlive(n)
			return
		default:
		}
		for i := 0; i < 100000; i++ {
			n = n*1664525 + 1013904223
		}
	}
}

func fixtureContention(ctx context.Context, mutex *sync.Mutex) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		mutex.Lock()
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
		mutex.Unlock()
	}
}
