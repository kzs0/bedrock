package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/kzs0/bedrock"
	"github.com/kzs0/bedrock/metric/prometheus"
)

var fixtureScenarios = make(map[string]func(fixtureConfig) error)

func scenarioOptions(cfg fixtureConfig, target any) error {
	if len(cfg.ScenarioConfig) == 0 {
		return nil
	}
	return json.Unmarshal(cfg.ScenarioConfig, target)
}

func serveScenario(ctx context.Context, b *bedrock.Bedrock, cleanup func(), app http.Handler, control *http.ServeMux, metrics http.Handler) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	stop := make(chan struct{})
	var once sync.Once
	if control == nil {
		control = http.NewServeMux()
	}
	stats := func() map[string]any {
		return map[string]any{"trace": b.TraceExportStats(), "profiles": b.ProfileStats(), "metrics": map[string]uint64{"DroppedSeries": b.Metrics().DroppedSeries()}}
	}
	defer func() { _ = json.NewEncoder(os.Stderr).Encode(map[string]any{"fixture_final_stats": stats()}) }()
	control.HandleFunc("/shutdown", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		once.Do(func() { close(stop) })
	})
	control.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	control.HandleFunc("/stats", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(stats()) })
	if app == nil {
		app = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	}
	if metrics == nil {
		metrics = prometheus.Handler(b.Metrics())
	}
	servers := []*http.Server{{Handler: app, ReadHeaderTimeout: time.Second}, {Handler: control, ReadHeaderTimeout: time.Second}, {Handler: metrics, ReadHeaderTimeout: time.Second}}
	urls := make([]string, 0, len(servers))
	for _, srv := range servers {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		defer func() { _ = listener.Close() }()
		urls = append(urls, "http://"+listener.Addr().String())
		go func() {
			if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				once.Do(func() { close(stop) })
			}
		}()
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]string{"app_url": urls[0], "control_url": urls[1], "metrics_url": urls[2]}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
	case <-stop:
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	for _, srv := range servers {
		_ = srv.Shutdown(shutdownCtx)
	}
	if cleanup != nil {
		cleanup()
		return nil
	}
	return b.Shutdown(shutdownCtx)
}
