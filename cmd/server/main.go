package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/term"

	"github.com/X9st/TianFaSRC/internal/agent"
	"github.com/X9st/TianFaSRC/internal/api"
	"github.com/X9st/TianFaSRC/internal/auth"
	"github.com/X9st/TianFaSRC/internal/config"
	"github.com/X9st/TianFaSRC/internal/llm"
	"github.com/X9st/TianFaSRC/internal/secret"
	"github.com/X9st/TianFaSRC/internal/store/postgres"
	"github.com/X9st/TianFaSRC/internal/system"
	"github.com/X9st/TianFaSRC/internal/task"
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
	var secrets *secret.Store
	var scheduler *agent.Scheduler
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
		var storedSecrets int
		if err := pool.QueryRow(rootCtx, `SELECT count(*) FROM secret_values`).Scan(&storedSecrets); err != nil {
			logger.Error("check secret store", "error", err)
			os.Exit(1)
		}
		if storedSecrets > 0 && !secret.Exists(cfg.DataDir) {
			logger.Error("master key missing for existing encrypted credentials")
			os.Exit(1)
		}
		secured, err := secret.Open(cfg.DataDir)
		if err != nil {
			logger.Error("open secret store", "error", err)
			os.Exit(1)
		}
		secrets = &secured
		profiles := llm.Store{Pool: pool, Secrets: secured}
		scheduler = &agent.Scheduler{Engine: agent.Engine{Pool: pool, Profiles: profiles, Router: llm.Router{Store: profiles}}, Logger: logger}
		if len(os.Args) > 1 && os.Args[1] == "init-admin" {
			if err := initAdmin(rootCtx, pool); err != nil {
				logger.Error("initialize admin", "error", err)
				os.Exit(1)
			}
			return
		}
	} else {
		logger.Warn("database is not configured; readiness will remain unavailable")
	}
	if pool != nil {
		if scheduler != nil {
			go scheduler.Run(rootCtx)
		}
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-rootCtx.Done():
					return
				case <-ticker.C:
					ctx, cancel := context.WithTimeout(rootCtx, 5*time.Second)
					_, err := (task.Store{Pool: pool}).ProjectActivities(ctx, 100)
					cancel()
					if err != nil && rootCtx.Err() == nil {
						logger.Error("project activities", "error", err)
					}
				}
			}
		}()
	}

	server := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: api.NewHandler(api.Options{
			Logger:    logger,
			Version:   version,
			Readiness: system.Readiness{Database: readinessDatabase(pool), DataDir: cfg.DataDir},
			Pool:      pool,
			Secrets:   secrets,
			Scheduler: scheduler,
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

func initAdmin(ctx context.Context, pool *pgxpool.Pool) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("init-admin requires an interactive terminal")
	}
	reader := bufio.NewReader(os.Stdin)
	fmt.Fprint(os.Stderr, "Admin email: ")
	email, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	fmt.Fprint(os.Stderr, "Admin password: ")
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	user, err := (auth.Store{Pool: pool}).InitAdmin(ctx, strings.TrimSpace(email), string(password))
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Initialized admin:", user.Email)
	return nil
}
