package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kzs0/bedrock"
	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/env"
)

type Config struct {
	Bedrock  bedrock.Config
	LoopTerm time.Duration `env:"LOOP_TERM" envDefault:"10s"`
}

func main() {
	ctx := context.Background()
	cfg, err := env.Parse[Config]()
	if err != nil {
		// Use defaults if config parsing fails
		cfg = Config{
			Bedrock:  bedrock.DefaultConfig(),
			LoopTerm: 10 * time.Second,
		}
	}

	cfg.Bedrock.Service = "example-service"
	cfg.Bedrock.LogFormat = "text"

	// Initialize bedrock - obs server starts automatically if enabled in config
	ctx, close := bedrock.Init(ctx,
		bedrock.WithConfig(cfg.Bedrock),
		bedrock.WithStaticAttrs(
			attr.String("env", "development"),
			attr.String("bedrock.version", "0.1.0"),
		),
	)
	defer close()

	// Demonstrate different log levels
	bedrock.Info(ctx, "Observability server listening on :9090",
		attr.String("server.address", ":9090"),
		attr.String("endpoints", "metrics, health, pprof"))

	bedrock.Debug(ctx, "Available endpoints",
		attr.String("metrics", "http://localhost:9090/metrics"),
		attr.String("health", "http://localhost:9090/health"),
		attr.String("pprof", "http://localhost:9090/debug/pprof/"))

	bedrock.Info(ctx, "Manual profiling commands available",
		attr.String("cpu_profile", "curl -o cpu.prof http://localhost:9090/debug/pprof/profile?seconds=30"),
		attr.String("heap_profile", "curl -o heap.prof http://localhost:9090/debug/pprof/heap"),
		attr.String("goroutine_profile", "curl -o goroutine.prof http://localhost:9090/debug/pprof/goroutine"))

	bedrock.Info(ctx, "Continuous profiling setup",
		attr.String("compose_start", "docker-compose up -d"),
		attr.String("grafana_url", "http://localhost:3000"),
		attr.String("scrape_interval", "15s"))

	// Setup HTTP server with security timeouts
	mux := http.NewServeMux()
	mux.HandleFunc("/users", handleUsers)

	handler := bedrock.HTTPMiddleware(ctx, mux)

	appServer := &http.Server{
		Addr:    ":8080",
		Handler: handler,
		// Security timeouts to prevent DoS attacks
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MB
	}

	// Start background loop
	go func() {
		if err := loop(ctx, cfg.LoopTerm); err != nil {
			bedrock.Error(ctx, "Loop error", attr.Error(err))
		}
	}()

	// Start application server
	go func() {
		bedrock.Info(ctx, "Application server listening on :8080",
			attr.String("server.address", ":8080"),
			attr.String("server.type", "application"))
		if err := appServer.ListenAndServe(); err != http.ErrServerClosed {
			bedrock.Error(ctx, "Application server error", attr.Error(err))
		}
	}()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh

	bedrock.Info(ctx, "Received shutdown signal",
		attr.String("signal", sig.String()))

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := appServer.Shutdown(shutdownCtx); err != nil {
		bedrock.Error(ctx, "Application server shutdown error", attr.Error(err))
	}

	bedrock.Info(ctx, "Shutdown complete")
}

func handleUsers(w http.ResponseWriter, r *http.Request) {
	op, ctx := bedrock.Operation(r.Context(), "http.get_users")
	defer op.Done()

	op.Register(ctx, attr.Int("user_count", 42))

	// Demonstrate convenient logging API (includes static attributes automatically)
	bedrock.Info(ctx, "processing user request", attr.String("path", r.URL.Path))

	// Simulate work
	result, err := doWork(ctx)
	if err != nil {
		op.Register(ctx, attr.Error(err))
		bedrock.Error(ctx, "request failed", attr.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	bedrock.Info(ctx, "request completed successfully", attr.String("result", result))
	_, _ = fmt.Fprintf(w, "Result: %s\n", result)
}

func loop(ctx context.Context, term time.Duration) error {
	source, ctx := bedrock.Source(ctx, "source.loop")
	defer source.Done()

	// Demonstrate convenient metric creation API
	loopCounter := bedrock.Counter(ctx, "background_loop_iterations", "Total loop iterations")
	activeGauge := bedrock.Gauge(ctx, "background_active", "Whether background loop is active")
	activeGauge.Set(1)
	defer activeGauge.Set(0)

	ticker := time.NewTicker(term)
	defer ticker.Stop()

	for {
		// Sources often work on aggregates since they never "resolve"
		source.Aggregate(ctx, attr.Sum("loops", 1))

		select {
		case <-ctx.Done():
			bedrock.Info(ctx, "background loop stopping")
			return nil
		case <-ticker.C:
			loopCounter.Inc()
			bedrock.Debug(ctx, "background loop tick", attr.Duration("interval", term))

			op, ctx := bedrock.Operation(ctx, "inner.loop")

			op.Register(ctx,
				attr.Int("test_count", 1),
				attr.String("test_string", "test"),
			)

			helper(ctx)

			op.Done()
		}
	}
}

func helper(ctx context.Context) {
	step := bedrock.Step(ctx, "helper")
	defer step.Done()

	step.Register(ctx, attr.Int("helper_count", 1))

	// Demonstrate different log levels
	bedrock.Debug(ctx, "Helper function started", attr.String("step", "helper"))

	// Simulate a warning condition
	threshold := 0.8
	currentUsage := 0.85
	if currentUsage > threshold {
		bedrock.Warn(ctx, "Resource usage above threshold",
			attr.Float64("current_usage", currentUsage),
			attr.Float64("threshold", threshold),
			attr.String("resource", "memory"))
	}

	bedrock.Debug(ctx, "Helper function completed", attr.Int("operations_performed", 1))
}

// doWork demonstrates nested operations and convenient metrics API
func doWork(ctx context.Context) (string, error) {
	op, ctx := bedrock.Operation(ctx, "db.query",
		bedrock.Attrs(
			attr.String("db.system", "postgresql"),
			attr.String("db.statement", "SELECT * FROM users"),
		),
		bedrock.MetricLabels("db.system"),
	)
	defer op.Done()

	// Demonstrate convenient metrics API with labels
	queryHistogram := bedrock.Histogram(ctx, "custom_query_duration_ms",
		"Custom query duration in milliseconds", nil, "db_system")

	start := time.Now()
	bedrock.Debug(ctx, "executing database query")

	// Simulate database work
	time.Sleep(50 * time.Millisecond)

	duration := time.Since(start)
	queryHistogram.With(attr.String("db_system", "postgresql")).Observe(float64(duration.Milliseconds()))

	bedrock.Info(ctx, "database query completed",
		attr.Duration("duration", duration),
		attr.String("db.system", "postgresql"))

	return "success", nil
}
