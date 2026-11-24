package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	t.Setenv("TIANFA_HTTP_ADDR", "")
	t.Setenv("TIANFA_PROXY_ADDR", "")
	t.Setenv("TIANFA_DATABASE_URL", "")
	t.Setenv("TIANFA_DATA_DIR", "")
	t.Setenv("TIANFA_LOG_LEVEL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.ProxyAddr != "127.0.0.1:8081" {
		t.Fatalf("ProxyAddr = %q", cfg.ProxyAddr)
	}
	if cfg.DataDir != "./data" {
		t.Fatalf("DataDir = %q", cfg.DataDir)
	}
}

func TestLoadRejectsInvalidLogLevel(t *testing.T) {
	t.Setenv("TIANFA_LOG_LEVEL", "verbose")
	if _, err := Load(); err == nil {
		t.Fatal("Load() expected an error")
	}
}
