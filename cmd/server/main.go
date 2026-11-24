package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/X9st/TianFaSRC/internal/api"
	"github.com/X9st/TianFaSRC/internal/config"
	"github.com/X9st/TianFaSRC/internal/store/postgres"
	"github.com/X9st/TianFaSRC/internal/system"
)

var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		_, _ = os.Stderr.WriteString("invalid configuration: " + err.Error() + "\n")
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var pool *pgxpool.Pool
	if cfg.DatabaseURL != "" {
		pool, err = pgxpool.New(rootCtx, cfg.DatabaseURL)
		if err != nil {
			logger.Error("create database pool", "error", err)
			os.Exit(1)
		}
		defer pool.Close()
		if err := postgres.Migrate(rootCtx, pool); err != nil {
			logger.Error("run database migrations", "error", err)
			os.Exit(1)
		}
		logger.Info("database migrations complete")
	} else {
		logger.Warn("database is not configured; readiness will remain unavailable")
	}

	server := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: api.NewHandler(api.Options{
			Logger:    logger,
			Version:   version,
			Readiness: system.Readiness{Database: readinessDatabase(pool), DataDir: cfg.DataDir},
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("server listening", "http_addr", cfg.HTTPAddr, "proxy_addr", cfg.ProxyAddr, "version", version)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-rootCtx.Done():
		logger.Info("shutdown requested", "reason", rootCtx.Err())
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownPeriod)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
			os.Exit(1)
		}
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "error", err)
			os.Exit(1)
		}
	}
	logger.Info("server stopped")
}

func readinessDatabase(pool *pgxpool.Pool) system.Database {
	if pool == nil {
		return nil
	}
	return pool
}
