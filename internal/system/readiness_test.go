package system

import (
	"context"
	"errors"
	"testing"
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func TestReadinessAllUp(t *testing.T) {
	checks := (Readiness{Database: fakeDB{}, DataDir: t.TempDir()}).Checks(context.Background())
	if !AllUp(checks) {
		t.Fatalf("AllUp() = false, checks = %#v", checks)
	}
}

func TestReadinessReportsDatabaseFailure(t *testing.T) {
	checks := (Readiness{Database: fakeDB{err: errors.New("offline")}, DataDir: t.TempDir()}).Checks(context.Background())
	if AllUp(checks) {
		t.Fatal("AllUp() = true")
	}
	if checks["database"].Detail != "database is unavailable" {
		t.Fatalf("unexpected detail %q", checks["database"].Detail)
	}
}
