package task_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/X9st/TianFaSRC/internal/agent"
	"github.com/X9st/TianFaSRC/internal/api"
	"github.com/X9st/TianFaSRC/internal/auth"
	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/llm"
	"github.com/X9st/TianFaSRC/internal/secret"
	"github.com/X9st/TianFaSRC/internal/store/postgres"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Run with TIANFA_PERF_DATABASE_URL on the 8 vCPU/16 GB reference host.
// The test uses an isolated database and leaves development data untouched.
func TestPhase7CorePerformance(t *testing.T) {
	base := os.Getenv("TIANFA_PERF_DATABASE_URL")
	if base == "" {
		t.Skip("set TIANFA_PERF_DATABASE_URL on the reference host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	adminConfig, err := pgx.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	dbName := "tianfa_perf_" + strings.ReplaceAll(identity.NewID(), "-", "")[16:]
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Database = dbName
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		pool.Close()
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+dbName); err != nil {
			t.Errorf("drop isolated performance database: %v", err)
		}
	}()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	operator, err := (auth.Store{Pool: pool}).InitAdmin(ctx, "perf@example.invalid", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	store := task.Store{Pool: pool}
	detail, err := store.Create(ctx, operator.OrganizationID, operator.ID, task.CreateInput{
		Title: "Phase 7 performance fixture", Goals: []task.GoalInput{{Text: "review local data", Priority: 5}},
		Scope: struct {
			Entries []task.ScopeEntryInput `json:"entries"`
		}{Entries: []task.ScopeEntryInput{{Effect: "allow", Kind: "domain", Value: "example.invalid"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO assets(id,organization_id,kind,canonical_key,display_value,raw_value)
		SELECT md5('asset-'||g::text)::uuid,$1,'subdomain','host-'||g||'.example.invalid','host-'||g||'.example.invalid','host-'||g||'.example.invalid'
		FROM generate_series(1,100000) g`, operator.OrganizationID); err != nil {
		t.Fatal(err)
	}
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM exploration_nodes WHERE task_id=$1`, detail.Task.ID).Scan(&existing); err != nil {
		t.Fatal(err)
	}
	var existingEdges int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM exploration_edges WHERE task_id=$1`, detail.Task.ID).Scan(&existingEdges); err != nil {
		t.Fatal(err)
	}
	if existing > 10000 {
		t.Fatalf("fixture has %d initial nodes", existing)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO exploration_nodes(id,task_id,kind,state,summary)
		SELECT md5('explore-'||g::text)::uuid,$1,'hint','open','hint '||g FROM generate_series(1,$2) g`, detail.Task.ID, 10000-existing); err != nil {
		t.Fatal(err)
	}
	var originID string
	if err := pool.QueryRow(ctx, `SELECT id FROM exploration_nodes WHERE task_id=$1 AND kind='origin'`, detail.Task.ID).Scan(&originID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO exploration_edges(task_id,from_node_id,to_node_id,kind)
		SELECT $1,$2,md5('explore-'||g::text)::uuid,'spawns' FROM generate_series(1,$3) g`, detail.Task.ID, originID, 10000-existing); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "ANALYZE assets"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "ANALYZE exploration_nodes"); err != nil {
		t.Fatal(err)
	}
	_, token, csrf, err := (auth.Store{Pool: pool}).Login(ctx, "perf@example.invalid", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	handler := api.NewHandler(api.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Pool: pool})
	call := func(path string) error {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: "tianfa_session", Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusOK || response.Body.Len() == 0 {
			return fmt.Errorf("GET %s returned %d (%d bytes)", path, response.Code, response.Body.Len())
		}
		return nil
	}
	graph, err := store.Graph(ctx, operator.OrganizationID, detail.Task.ID)
	if err != nil || len(graph.Nodes) != 10000 || len(graph.Edges) != existingEdges+10000-existing {
		t.Fatalf("fixture graph: %d nodes %d edges %v", len(graph.Nodes), len(graph.Edges), err)
	}
	assetP95 := measureP95(t, 30, func() error {
		return call("/api/v1/assets?kind=subdomain&limit=50")
	})
	overviewP95 := measureP95(t, 30, func() error {
		if err := call("/api/v1/tasks/" + detail.Task.ID + "/graph"); err != nil {
			return err
		}
		return call("/api/v1/tasks/" + detail.Task.ID + "/coverage")
	})
	t.Logf("100k assets filtered list P95=%s; 10k node graph+coverage P95=%s", assetP95, overviewP95)
	if os.Getenv("TIANFA_PERF_ENFORCE") == "1" && (assetP95 >= 500*time.Millisecond || overviewP95 >= time.Second) {
		t.Fatalf("Phase 7 reference environment performance gate failed")
	}
	if _, err := pool.Exec(ctx, `UPDATE tasks SET status='running',worker_limit=50 WHERE id=$1`, detail.Task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE scheduler_capacity SET max_workers=50 WHERE singleton=true`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO exploration_nodes(id,task_id,kind,state,summary)
		SELECT md5('perf-worker-'||g::text)::uuid,$1,'intent','open','worker '||g FROM generate_series(1,50) g`, detail.Task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO task_intents(id,task_id,goal_id,objective,success_criteria,dedup_key)
		SELECT md5('perf-worker-'||g::text)::uuid,$1,$2,'worker '||g,'local result','perf-worker-'||g FROM generate_series(1,50) g`, detail.Task.ID, detail.Goals[0].ID); err != nil {
		t.Fatal(err)
	}
	profiles := llm.Store{Pool: pool}
	if _, err := profiles.ActivePrompt(ctx, operator.OrganizationID, operator.ID, "worker"); err != nil {
		t.Fatal(err)
	}
	scheduler := agent.Scheduler{Engine: agent.Engine{Pool: pool, Profiles: profiles}}
	for i := 0; i < 50; i++ {
		if _, claimed, err := scheduler.ClaimWorker(ctx); err != nil || !claimed {
			t.Fatalf("claim worker %d: claimed=%t err=%v", i+1, claimed, err)
		}
	}
	// Keep the leases active throughout the control API sampling window.
	if _, err := pool.Exec(ctx, `UPDATE worker_leases SET expires_at=now()+interval '10 minutes' WHERE task_id=$1`, detail.Task.ID); err != nil {
		t.Fatal(err)
	}
	var active int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_leases WHERE task_id=$1 AND expires_at>now()`, detail.Task.ID).Scan(&active); err != nil || active != 50 {
		t.Fatalf("active workers=%d err=%v", active, err)
	}
	secrets, err := secret.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	controlHandler := api.NewHandler(api.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Pool: pool, Secrets: &secrets, Scheduler: &scheduler})
	control := func(command string) error {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+detail.Task.ID+":"+command, nil)
		req.AddCookie(&http.Cookie{Name: "tianfa_session", Value: token})
		req.Header.Set("X-CSRF-Token", csrf)
		response := httptest.NewRecorder()
		controlHandler.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			return fmt.Errorf("%s returned %d: %s", command, response.Code, response.Body.String())
		}
		return nil
	}
	controlP95 := measureP95(t, 30, func() error {
		// The background scheduler normally admits a resumed queued task.
		// Simulate that transition between independent control cycles.
		if _, err := pool.Exec(ctx, `UPDATE tasks SET status='running' WHERE id=$1 AND status='queued'`, detail.Task.ID); err != nil {
			return err
		}
		if err := control("pause"); err != nil {
			return err
		}
		return control("resume")
	})
	t.Logf("50 active Worker pause+resume API cycle P95=%s", controlP95)
	if os.Getenv("TIANFA_PERF_ENFORCE") == "1" && controlP95 >= 500*time.Millisecond {
		t.Fatalf("Phase 7 50-worker control API reference performance gate failed")
	}
	cursorID := identity.NewID()
	if _, err := pool.Exec(ctx, `INSERT INTO domain_events(id,organization_id,task_id,event_type,payload,occurred_at)
		VALUES($1,$2,$3,'perf.cursor','{}',now()-interval '1 hour')`, cursorID, operator.OrganizationID, detail.Task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO domain_events(id,organization_id,task_id,event_type,payload,occurred_at)
		SELECT md5('perf-event-'||g::text)::uuid,$1,$2,'perf.event','{}',now()-interval '30 minutes'+g*interval '1 microsecond'
		FROM generate_series(1,10000) g`, operator.OrganizationID, detail.Task.ID); err != nil {
		t.Fatal(err)
	}
	expected := make(map[string]bool, 10000)
	rows, err := pool.Query(ctx, `SELECT id FROM domain_events WHERE task_id=$1 AND event_type='perf.event'`, detail.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		expected[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(expected) != 10000 {
		t.Fatalf("SSE fixture events=%d err=%v", len(expected), err)
	}
	server := httptest.NewServer(controlHandler)
	defer server.Close()
	client := &http.Client{Timeout: 15 * time.Second}
	seen := make(map[string]bool, 10000)
	var firstBatch time.Duration
	for connection := 0; connection < 2; connection++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/tasks/"+detail.Task.ID+"/events", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: "tianfa_session", Value: token})
		req.Header.Set("Last-Event-ID", cursorID)
		started := time.Now()
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			t.Fatalf("SSE reconnect returned %d", response.StatusCode)
		}
		scanner := bufio.NewScanner(response.Body)
		received := 0
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "id: ") {
				continue
			}
			id := strings.TrimPrefix(line, "id: ")
			if seen[id] {
				t.Fatalf("duplicate SSE event %s", id)
			}
			if !expected[id] {
				t.Fatalf("unexpected SSE event %s", id)
			}
			seen[id] = true
			delete(expected, id)
			cursorID = id
			received++
			if connection == 0 && received == 500 {
				firstBatch = time.Since(started)
			}
			if received == 5000 {
				break
			}
		}
		scanErr := scanner.Err()
		response.Body.Close()
		if scanErr != nil || received != 5000 {
			t.Fatalf("SSE connection %d received %d: %v", connection+1, received, scanErr)
		}
	}
	t.Logf("10k SSE replay across reconnect: first 500 events=%s, unique events=%d", firstBatch, len(seen))
	if len(expected) != 0 || (os.Getenv("TIANFA_PERF_ENFORCE") == "1" && firstBatch >= 2*time.Second) {
		t.Fatalf("Phase 7 SSE replay reference gate failed")
	}
}

func measureP95(t *testing.T, count int, operation func() error) time.Duration {
	t.Helper()
	for i := 0; i < 3; i++ {
		if err := operation(); err != nil {
			t.Fatal(err)
		}
	}
	samples := make([]time.Duration, count)
	for i := range samples {
		started := time.Now()
		if err := operation(); err != nil {
			t.Fatal(err)
		}
		samples[i] = time.Since(started)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return samples[(count*95+99)/100-1]
}
