package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultProxyAddr = "127.0.0.1:8081"
	defaultDataDir   = "./data"
)

type Config struct {
	HTTPAddr       string
	ProxyAddr      string
	DatabaseURL    string
	DataDir        string
	LogLevel       slog.Level
	ShutdownPeriod time.Duration
}

func Load() (Config, error) {
	level, err := parseLogLevel(value("TIANFA_LOG_LEVEL", "info"))
	if err != nil {
		return Config{}, err
	}

	return Config{
		HTTPAddr:       value("TIANFA_HTTP_ADDR", defaultHTTPAddr),
		ProxyAddr:      value("TIANFA_PROXY_ADDR", defaultProxyAddr),
		DatabaseURL:    strings.TrimSpace(os.Getenv("TIANFA_DATABASE_URL")),
		DataDir:        value("TIANFA_DATA_DIR", defaultDataDir),
		LogLevel:       level,
		ShutdownPeriod: 15 * time.Second,
	}, nil
}

func value(key, fallback string) string {
	if got := strings.TrimSpace(os.Getenv(key)); got != "" {
		return got
	}
	return fallback
}

func parseLogLevel(raw string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		return 0, fmt.Errorf("TIANFA_LOG_LEVEL: %w", err)
	}
	return level, nil
}
