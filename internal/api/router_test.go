package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/X9st/TianFaSRC/internal/system"
)

type testDB struct{ err error }

func (d testDB) Ping(context.Context) error { return d.err }

func testHandler(t *testing.T, database system.Database) http.Handler {
	t.Helper()
	return NewHandler(Options{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
		Readiness: system.Readiness{
			Database: database,
			DataDir:  t.TempDir(),
		},
	})
}

func TestLiveness(t *testing.T) {
	recorder := httptest.NewRecorder()
	testHandler(t, nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if recorder.Header().Get(requestIDHeader) == "" {
		t.Fatal("missing request ID")
	}
}

func TestReadinessUnavailableWithoutDatabase(t *testing.T) {
	recorder := httptest.NewRecorder()
	testHandler(t, nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
	var response healthResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "not_ready" {
		t.Fatalf("status body = %q", response.Status)
	}
}

func TestReadinessOK(t *testing.T) {
	recorder := httptest.NewRecorder()
	testHandler(t, testDB{}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestUnknownAPIReturnsProblem(t *testing.T) {
	recorder := httptest.NewRecorder()
	testHandler(t, nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/unknown", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q", got)
	}
}
