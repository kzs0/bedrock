package main

import (
	"context"
	"fmt"
	"log"
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
		cfg.Bedrock.Service = "example-service"
		// Enable observability server
		cfg.Bedrock.ServerEnabled = true
	}

	// Initialize bedrock - obs server starts automatically if enabled in config
	ctx, close := bedrock.Init(ctx,
		bedrock.WithConfig(cfg.Bedrock),
		bedrock.WithStaticAttrs(
			attr.String("env", "development"),
			attr.String("bedrock.version", "0.1.0"),
		),
	)
	defer close()

	log.Println("Observability server listening on :9090")
	log.Println("  - Metrics: http://localhost:9090/metrics")
	log.Println("  - Health:  http://localhost:9090/health")
	log.Println("  - Pprof:   http://localhost:9090/debug/pprof/")
	log.Println("")
	log.Println("Profiling:")
	log.Println("  Manual profiling:")
	log.Println("    - CPU profile (30s):  curl -o cpu.prof http://localhost:9090/debug/pprof/profile?seconds=30")
	log.Println("    - Heap profile:       curl -o heap.prof http://localhost:9090/debug/pprof/heap")
	log.Println("    - Goroutine profile:  curl -o goroutine.prof http://localhost:9090/debug/pprof/goroutine")
	log.Println("    - Analyze profile:    go tool pprof cpu.prof")
	log.Println("")
	log.Println("  Continuous profiling with Pyroscope + Grafana:")
	log.Println("    1. Start: docker-compose up -d")
	log.Println("    2. View:  http://localhost:3000 (Grafana)")
	log.Println("    3. Pyroscope will scrape pprof endpoints every 15s")
	log.Println("")

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
			log.Printf("Loop error: %v", err)
		}
	}()

	// Start application server
	go func() {
		log.Println("Application server listening on :8080")
		if err := appServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("Application server error: %v", err)
		}
	}()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := appServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Application server shutdown error: %v", err)
	}

	log.Println("Goodbye!")
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
