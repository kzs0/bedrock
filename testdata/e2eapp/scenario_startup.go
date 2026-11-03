package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kzs0/bedrock"
	"github.com/kzs0/bedrock/attr"
	"github.com/kzs0/bedrock/env"
	"github.com/kzs0/bedrock/privacy"
	"github.com/kzs0/bedrock/server"
)

type startupOptions struct {
	Mode      string
	Service   string
	Metadata  bool
	Override  bool
	Metrics   bool
	Automatic bool
	Address   string
}

func init() { fixtureScenarios["startup"] = runStartupScenario }

func runStartupScenario(cfg fixtureConfig) error {
	var opts startupOptions
	if err := scenarioOptions(cfg, &opts); err != nil {
		return err
	}
	bc := bedrock.DefaultConfig()
	bc.Service = "startup-service"
	bc.ServerEnabled = false
	bc.RuntimeMetrics = false
	bc.DisableEnvironmentDetection = true
	bc.TraceURL = cfg.TraceEndpoint
	bc.TraceProtocol = "http"
	bc.ShutdownTimeout = 2 * time.Second
	if opts.Service != "" {
		bc.Service = opts.Service
	}
	ctx := context.Background()
	var b *bedrock.Bedrock
	var cleanup func()
	var err error
	switch opts.Mode {
	case "implicit":
		ctx, cleanup = bedrock.Init(ctx)
		b = bedrock.FromContext(ctx)
	case "fromenv", "mustenv":
		if opts.Mode == "mustenv" {
			bc = bedrock.MustFromEnv()
		} else {
			bc, err = bedrock.FromEnv()
			if err != nil {
				return err
			}
		}
		b, err = bedrock.New(bc)
		if err != nil {
			return err
		}
		ctx = bedrock.WithBedrock(ctx, b)
	default:
		bc.DisableEnvironmentDetection = opts.Mode != "detection"
		if opts.Metadata {
			bc.DataPolicy = &privacy.Policy{ResourceKeys: []string{"service.name", "service.version", "process.pid", "process.executable.name", "host.name", "host.id", "k8s.pod.name", "k8s.namespace", "k8s.node.name", "k8s.container.name", "container.id", "cloud.provider", "cloud.region"}}
		}
		var attrs []attr.Attr
		if opts.Override {
			attrs = append(attrs, attr.String("host.name", "explicit-host"), attr.String("service.version", "explicit-version"))
		}
		if opts.Automatic {
			bc.ServerEnabled = true
			bc.ServerAddr = opts.Address
			bc.ServerMetrics = opts.Metrics
		}
		ctx, cleanup = bedrock.Init(ctx, bedrock.WithConfig(bc), bedrock.WithStaticAttrs(attrs...))
		b = bedrock.FromContext(ctx)
	}
	app := http.NewServeMux()
	app.HandleFunc("/environment", startupEnvironment)
	app.HandleFunc("/config", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"Service": bc.Service, "TraceSampleRate": bc.TraceSampleRate, "ServerEnabled": bc.ServerEnabled})
	})
	app.HandleFunc("/operation", func(w http.ResponseWriter, _ *http.Request) {
		op, _ := bedrock.Operation(ctx, "startup.operation")
		op.Done()
		w.WriteHeader(204)
	})
	control := http.NewServeMux()
	if opts.Mode == "server" {
		sc := server.Config{EnableMetrics: opts.Metrics}
		s := server.New(b.Metrics(), sc)
		ln, listenErr := net.Listen("tcp", "127.0.0.1:0")
		if listenErr != nil {
			return listenErr
		}
		done := make(chan error, 1)
		go func() { done <- s.Serve(ln) }()
		control.HandleFunc("/server", func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"url": "http://" + ln.Addr().String()})
		})
		prior := cleanup
		cleanup = func() {
			_ = s.Shutdown(context.Background())
			if serveErr := <-done; serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				log.Print(serveErr)
			}
			if prior != nil {
				prior()
			}
		}
	}
	if opts.Mode == "bind-failure" {
		s := server.New(b.Metrics(), server.Config{Addr: "127.0.0.1:bad-port"})
		bindErr := s.ListenAndServe()
		control.HandleFunc("/bind", func(w http.ResponseWriter, _ *http.Request) {
			message := ""
			if bindErr != nil {
				message = bindErr.Error()
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
		})
	}
	return serveScenario(ctx, b, cleanup, app, control, nil)
}

type startupValues struct {
	String   string        `env:"STARTUP_STRING" envDefault:"default-name"`
	Int      int           `env:"STARTUP_INT"`
	Int8     int8          `env:"STARTUP_INT8"`
	Int16    int16         `env:"STARTUP_INT16"`
	Int32    int32         `env:"STARTUP_INT32"`
	Int64    int64         `env:"STARTUP_INT64"`
	Uint     uint          `env:"STARTUP_UINT"`
	Uint8    uint8         `env:"STARTUP_UINT8"`
	Uint16   uint16        `env:"STARTUP_UINT16"`
	Uint32   uint32        `env:"STARTUP_UINT32"`
	Uint64   uint64        `env:"STARTUP_UINT64"`
	Uintptr  uintptr       `env:"STARTUP_UINTPTR"`
	Float32  float32       `env:"STARTUP_FLOAT32"`
	Float64  float64       `env:"STARTUP_FLOAT64"`
	Bool     bool          `env:"STARTUP_BOOL"`
	Duration time.Duration `env:"STARTUP_DURATION"`
	List     []int         `env:"STARTUP_LIST"`
	Map      map[int]bool  `env:"STARTUP_MAP"`
	Nested   struct {
		Value string `env:"VALUE,required"`
	} `envPrefix:"STARTUP_NESTED_"`
	Ignored struct {
		Value string `env:"VALUE,required"`
	} `env:"-"`
	Untagged string
	private  string `env:"STARTUP_PRIVATE"`
}

type startupEnvironmentRequest struct {
	Case   string
	Values map[string]string
}

func startupEnvironment(w http.ResponseWriter, r *http.Request) {
	var request startupEnvironmentRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&request); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "STARTUP_") {
			_ = os.Unsetenv(key)
		}
	}
	for key, value := range request.Values {
		if !strings.HasPrefix(key, "STARTUP_") {
			http.Error(w, "invalid environment key", 400)
			return
		}
		_ = os.Setenv(key, value)
	}
	value, err := startupParseEnvironment(request.Case)
	response := map[string]any{"value": value, "error": ""}
	if err != nil {
		response["error"] = err.Error()
	}
	_ = json.NewEncoder(w).Encode(response)
}

func startupParseEnvironment(name string) (any, error) {
	switch name {
	case "all":
		value, err := env.Parse[startupValues]()
		if value.private != "" {
			return nil, errors.New("unexported environment field changed")
		}
		return value, err
	case "prefix":
		return env.ParseWithPrefix[struct {
			Value string `env:"VALUE"`
		}]("STARTUP_PREFIX_")
	case "required":
		return env.Parse[struct {
			Value string `env:"STARTUP_VALUE,required"`
		}]()
	case "not-empty":
		return env.Parse[struct {
			Value string `env:"STARTUP_VALUE,notEmpty"`
		}]()
	case "int8":
		return env.Parse[struct {
			Value int8 `env:"STARTUP_VALUE"`
		}]()
	case "uint8":
		return env.Parse[struct {
			Value uint8 `env:"STARTUP_VALUE"`
		}]()
	case "float32":
		return env.Parse[struct {
			Value float32 `env:"STARTUP_VALUE"`
		}]()
	case "bool":
		return env.Parse[struct {
			Value bool `env:"STARTUP_VALUE"`
		}]()
	case "duration":
		return env.Parse[struct {
			Value time.Duration `env:"STARTUP_VALUE"`
		}]()
	case "slice":
		return env.Parse[struct {
			Value []int `env:"STARTUP_VALUE"`
		}]()
	case "map":
		return env.Parse[struct {
			Value map[int]bool `env:"STARTUP_VALUE"`
		}]()
	case "unsupported":
		return env.Parse[struct {
			Value *int `env:"STARTUP_VALUE"`
		}]()
	case "empty-tag":
		return env.Parse[struct {
			Value string `env:""`
		}]()
	case "unknown-option":
		return env.Parse[struct {
			Value string `env:"STARTUP_VALUE,wat"`
		}]()
	case "trailing-option":
		return env.Parse[struct {
			Value string `env:"STARTUP_VALUE,"`
		}]()
	case "skip-option":
		return env.Parse[struct {
			Value string `env:"-,required"`
		}]()
	case "nested-tag":
		return env.Parse[struct {
			Nested struct{ Value int } `env:",required"`
		}]()
	case "from-valid":
		return env.From(struct {
			Name   string `env:"STARTUP_NAME,required"`
			Nested struct {
				Count int `env:"COUNT,required"`
			} `envPrefix:"STARTUP_NESTED_"`
			Skip string `env:"-"`
		}{Name: "ready", Nested: struct {
			Count int `env:"COUNT,required"`
		}{Count: 3}})
	case "from-required":
		return env.From(struct {
			Value string `env:"STARTUP_VALUE,required"`
		}{})
	case "from-empty":
		return env.From(struct {
			Value string `env:"STARTUP_VALUE,notEmpty"`
		}{})
	case "from-nested":
		return env.From(struct {
			Nested struct {
				Value int `env:"VALUE,required"`
			} `envPrefix:"STARTUP_"`
		}{})
	case "from-invalid":
		return env.From(struct {
			Value string `env:"STARTUP_VALUE,wat"`
		}{})
	case "from-nonstring":
		return env.From(struct {
			Value int `env:"STARTUP_VALUE,notEmpty"`
		}{})
	default:
		return nil, errors.New("unknown environment case")
	}
}
