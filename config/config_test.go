package config

import (
	"os"
	"testing"
	"time"
)

type SimpleConfig struct {
	Host string `env:"HOST" envDefault:"localhost"`
	Port int    `env:"PORT" envDefault:"8080"`
}

type RequiredConfig struct {
	Name string `env:"NAME,required"`
}

type NestedConfig struct {
	App struct {
		Name string `env:"NAME"`
	} `envPrefix:"APP_"`
	DB struct {
		Host string `env:"HOST"`
		Port int    `env:"PORT"`
	} `envPrefix:"DB_"`
}

type TypesConfig struct {
	String   string        `env:"STRING"`
	Int      int           `env:"INT"`
	Int64    int64         `env:"INT64"`
	Float    float64       `env:"FLOAT"`
	Bool     bool          `env:"BOOL"`
	Duration time.Duration `env:"DURATION"`
	Strings  []string      `env:"STRINGS"`
}

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse[SimpleConfig]()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Host != "localhost" {
		t.Errorf("expected host 'localhost', got %q", cfg.Host)
	}
	if cfg.Port != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.Port)
	}
}

func TestParseFromEnv(t *testing.T) {
	_ = os.Setenv("HOST", "example.com")
	_ = os.Setenv("PORT", "3000")
	defer func() {
		_ = os.Unsetenv("HOST")
		_ = os.Unsetenv("PORT")
	}()

	cfg, err := Parse[SimpleConfig]()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Host != "example.com" {
		t.Errorf("expected host 'example.com', got %q", cfg.Host)
	}
	if cfg.Port != 3000 {
		t.Errorf("expected port 3000, got %d", cfg.Port)
	}
}

func TestParseRequired(t *testing.T) {
	_, err := Parse[RequiredConfig]()
	if err == nil {
		t.Fatal("expected error for missing required field")
	}

	_ = os.Setenv("NAME", "test")
	defer func() { _ = os.Unsetenv("NAME") }()

	cfg, err := Parse[RequiredConfig]()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Name != "test" {
		t.Errorf("expected name 'test', got %q", cfg.Name)
	}
}

func TestParseWithPrefix(t *testing.T) {
	_ = os.Setenv("MYAPP_HOST", "prefixed.com")
	_ = os.Setenv("MYAPP_PORT", "4000")
	defer func() {
		_ = os.Unsetenv("MYAPP_HOST")
		_ = os.Unsetenv("MYAPP_PORT")
	}()

	cfg, err := ParseWithPrefix[SimpleConfig]("MYAPP_")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Host != "prefixed.com" {
		t.Errorf("expected host 'prefixed.com', got %q", cfg.Host)
	}
	if cfg.Port != 4000 {
		t.Errorf("expected port 4000, got %d", cfg.Port)
	}
}

func TestParseNested(t *testing.T) {
	_ = os.Setenv("APP_NAME", "myapp")
	_ = os.Setenv("DB_HOST", "db.example.com")
	_ = os.Setenv("DB_PORT", "5432")
	defer func() {
		_ = os.Unsetenv("APP_NAME")
		_ = os.Unsetenv("DB_HOST")
		_ = os.Unsetenv("DB_PORT")
	}()

	cfg, err := Parse[NestedConfig]()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.App.Name != "myapp" {
		t.Errorf("expected app name 'myapp', got %q", cfg.App.Name)
	}
	if cfg.DB.Host != "db.example.com" {
		t.Errorf("expected db host 'db.example.com', got %q", cfg.DB.Host)
	}
	if cfg.DB.Port != 5432 {
		t.Errorf("expected db port 5432, got %d", cfg.DB.Port)
	}
}

func TestParseTypes(t *testing.T) {
	_ = os.Setenv("STRING", "hello")
	_ = os.Setenv("INT", "42")
	_ = os.Setenv("INT64", "9223372036854775807")
	_ = os.Setenv("FLOAT", "3.14")
	_ = os.Setenv("BOOL", "true")
	_ = os.Setenv("DURATION", "5s")
	_ = os.Setenv("STRINGS", "a,b,c")
	defer func() {
		_ = os.Unsetenv("STRING")
		_ = os.Unsetenv("INT")
		_ = os.Unsetenv("INT64")
		_ = os.Unsetenv("FLOAT")
		_ = os.Unsetenv("BOOL")
		_ = os.Unsetenv("DURATION")
		_ = os.Unsetenv("STRINGS")
	}()

	cfg, err := Parse[TypesConfig]()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.String != "hello" {
		t.Errorf("expected string 'hello', got %q", cfg.String)
	}
	if cfg.Int != 42 {
		t.Errorf("expected int 42, got %d", cfg.Int)
	}
	if cfg.Int64 != 9223372036854775807 {
		t.Errorf("expected int64 max, got %d", cfg.Int64)
	}
	if cfg.Float != 3.14 {
		t.Errorf("expected float 3.14, got %f", cfg.Float)
	}
	if !cfg.Bool {
		t.Error("expected bool true")
	}
	if cfg.Duration != 5*time.Second {
		t.Errorf("expected duration 5s, got %v", cfg.Duration)
	}
	if len(cfg.Strings) != 3 || cfg.Strings[0] != "a" {
		t.Errorf("expected strings [a,b,c], got %v", cfg.Strings)
	}
}

func TestFrom(t *testing.T) {
	cfg := RequiredConfig{Name: "test"}

	validated, err := From(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if validated.Name != "test" {
		t.Errorf("expected name 'test', got %q", validated.Name)
	}
}
