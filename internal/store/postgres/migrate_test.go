package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrationVersion(t *testing.T) {
	got, err := migrationVersion("000042_add_tasks.sql")
	if err != nil {
		t.Fatalf("migrationVersion() error = %v", err)
	}
	if got != 42 {
		t.Fatalf("migrationVersion() = %d", got)
	}
}

func TestMigrationVersionRejectsInvalidName(t *testing.T) {
	if _, err := migrationVersion("bootstrap.sql"); err == nil {
		t.Fatal("migrationVersion() expected an error")
	}
}

func TestMigrateIdempotentAndConcurrent(t *testing.T) {
	baseURL := os.Getenv("TIANFA_TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("set TIANFA_TEST_DATABASE_URL to run the PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config, err := pgx.ParseConfig(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	name := "tianfa_phase0_" + hex.EncodeToString(suffix[:])
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s", name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, fmt.Sprintf("DROP DATABASE %s", name)); err != nil {
			t.Errorf("drop test database: %v", err)
		}
	})
	poolConfig, err := pgxpool.ParseConfig(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.Database = name
	poolConfig.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- Migrate(ctx, pool)
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent Migrate(): %v", err)
		}
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("repeat Migrate(): %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 9 {
		t.Fatalf("migration count = %d, want 9", count)
	}
	var metadataExists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('system_metadata') IS NOT NULL").Scan(&metadataExists); err != nil {
		t.Fatal(err)
	}
	if !metadataExists {
		t.Fatal("system_metadata was not created")
	}
}
