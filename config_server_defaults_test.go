package bedrock

import "testing"

func TestDefaultConfigUsesSafeServerDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ServerAddr != "127.0.0.1:9090" {
		t.Errorf("ServerAddr: got %q, want 127.0.0.1:9090", cfg.ServerAddr)
	}
	if cfg.ServerPprof {
		t.Error("ServerPprof should default to false")
	}

	serverCfg := cfg.serverConfig()
	if serverCfg.Addr != cfg.ServerAddr {
		t.Errorf("server Config Addr: got %q, want %q", serverCfg.Addr, cfg.ServerAddr)
	}
	if serverCfg.EnablePprof {
		t.Error("server Config EnablePprof should default to false")
	}
}

func TestFromEnvUsesSafeServerDefaults(t *testing.T) {
	clearBedrockEnv(t)
	t.Setenv("BEDROCK_SERVER_ADDR", "")
	t.Setenv("BEDROCK_SERVER_PPROF", "")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cfg.ServerAddr != "127.0.0.1:9090" {
		t.Errorf("ServerAddr: got %q, want 127.0.0.1:9090", cfg.ServerAddr)
	}
	if cfg.ServerPprof {
		t.Error("ServerPprof should default to false")
	}
}

func TestServerConfigPreservesExplicitOverrides(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ServerAddr = ":9191"
	cfg.ServerPprof = true

	serverCfg := cfg.serverConfig()
	if serverCfg.Addr != ":9191" {
		t.Errorf("Addr: got %q, want :9191", serverCfg.Addr)
	}
	if !serverCfg.EnablePprof {
		t.Error("EnablePprof: got false, want explicit true")
	}
}

func TestFromEnvPreservesExplicitServerOverrides(t *testing.T) {
	clearBedrockEnv(t)
	t.Setenv("BEDROCK_SERVER_ADDR", "0.0.0.0:9191")
	t.Setenv("BEDROCK_SERVER_PPROF", "true")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cfg.ServerAddr != "0.0.0.0:9191" {
		t.Errorf("ServerAddr: got %q, want 0.0.0.0:9191", cfg.ServerAddr)
	}
	if !cfg.ServerPprof {
		t.Error("ServerPprof: got false, want explicit true")
	}
}
