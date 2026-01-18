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
	"github.com/kzs0/bedrock/config"
)

type Config struct {
	Bedrock  bedrock.Config
	LoopTerm time.Duration `env:"LOOP_TERM" envDefault:"10s"`
}

func main() {
	ctx := context.Background()
	cfg, err := config.Parse[Config]()
	if err != nil {
		// Use defaults if config parsing fails
		cfg = Config{
			Bedrock:  bedrock.DefaultConfig(),
			LoopTerm: 10 * time.Second,
		}
		cfg.Bedrock.ServiceName = "example-service"
	}

	// Initialize bedrock
	ctx, close := bedrock.Init(ctx,
		bedrock.WithConfig(cfg.Bedrock),
		bedrock.WithStaticAttrs(
			attr.String("env", "development"),
			attr.String("version", "1.0.0"),
		),
	)
	defer close()

	// Start observability server with default security settings
	b := bedrock.FromContext(ctx)
	obsServer := b.NewServer(bedrock.DefaultServerConfig())
	go func() {
		log.Println("Observability server listening on :9090")
		log.Println("  - Metrics: http://localhost:9090/metrics")
		log.Println("  - Pprof:   http://localhost:9090/debug/pprof/")
		if err := obsServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("Observability server error: %v", err)
		}
	}()

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

	appServer.Shutdown(shutdownCtx)
	obsServer.Shutdown(shutdownCtx)

	log.Println("Goodbye!")
}

func handleUsers(w http.ResponseWriter, r *http.Request) {
	op, ctx := bedrock.Operation(r.Context(), "http.get_users")
	defer op.Done()

	op.Register(ctx, attr.Int("user_count", 42))

	// Simulate work
	result, err := doWork(ctx)
	if err != nil {
		op.Register(ctx, attr.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Fprintf(w, "Result: %s\n", result)
}

func loop(ctx context.Context, term time.Duration) error {
	source, ctx := bedrock.Source(ctx, "source.loop")
	defer source.Done()

	ticker := time.NewTicker(term)
	defer ticker.Stop()

	for {
		// Sources often work on aggregates since they never "resolve"
		source.Aggregate(ctx, attr.Sum("loops", 1))

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
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
	step := bedrock.NewStep(ctx, "helper")
	defer step.Done()

	step.Register(ctx, attr.Int("helper_count", 1))
}

// doWork demonstrates nested operations
func doWork(ctx context.Context) (string, error) {
	op, ctx := bedrock.Operation(ctx, "db.query",
		bedrock.Attrs(
			attr.String("db.system", "postgresql"),
			attr.String("db.statement", "SELECT * FROM users"),
		),
		bedrock.MetricLabels("db.system"),
	)
	defer op.Done()

	// Simulate database work
	time.Sleep(50 * time.Millisecond)

	return "success", nil
}
