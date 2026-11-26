package task

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/X9st/TianFaSRC/internal/identity"
)

var (
	ErrNotFound         = errors.New("task not found")
	ErrConflict         = errors.New("task version conflict")
	ErrInvalid          = errors.New("invalid task input")
	ErrModelUnavailable = errors.New("请先配置系统默认主备链或任务模型链，并启用至少一个模型 Profile")
)

type GoalInput struct {
	Text     string `json:"text"`
	Priority int    `json:"priority"`
}
type ConstraintInput struct {
	Effect string `json:"effect"`
	Text   string `json:"text"`
}
type ExecutionInput struct {
	PolicyMode  string     `json:"policy_mode"`
	WorkerLimit int        `json:"worker_limit"`
	DeadlineAt  *time.Time `json:"deadline_at"`
}
type CreateInput struct {
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Goals       []GoalInput `json:"goals"`
	Scope       struct {
		Entries []ScopeEntryInput `json:"entries"`
	} `json:"scope"`
	Constraints     []ConstraintInput `json:"constraints"`
	Execution       ExecutionInput    `json:"execution"`
	LLMProfileChain []string          `json:"llm_profile_chain,omitempty"`
}
type Task struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	Title          string     `json:"title"`
	Description    string     `json:"description"`
	Status         string     `json:"status"`
	PolicyMode     string     `json:"policy_mode"`
	WorkerLimit    int        `json:"worker_limit"`
	DeadlineAt     *time.Time `json:"deadline_at"`
	Version        int64      `json:"version"`
	ScopeVersion   int64      `json:"scope_version"`
	CreatedBy      string     `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}
type Goal struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	State     string    `json:"state"`
	Priority  int       `json:"priority"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
}
type Constraint struct {
	ID     string `json:"id"`
	Effect string `json:"effect"`
	Text   string `json:"text"`
	Source string `json:"source"`
}
type Detail struct {
	Task        Task         `json:"task"`
	Goals       []Goal       `json:"goals"`
	Constraints []Constraint `json:"constraints"`
	Scope       []ScopeEntry `json:"scope"`
}
type Node struct {
	ID             string    `json:"id"`
	Kind           string    `json:"kind"`
	State          string    `json:"state"`
	Summary        string    `json:"summary"`
	SourceIntentID *string   `json:"source_intent_id,omitempty"`
	RunID          *string   `json:"run_id,omitempty"`
	ToolCallID     *string   `json:"tool_call_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}
type Edge struct {
	FromNodeID string `json:"from_node_id"`
	ToNodeID   string `json:"to_node_id"`
	Kind       string `json:"kind"`
}
type Anchor struct {
	NodeID     string `json:"node_id"`
	AssetID    string `json:"asset_id"`
	AssetKind  string `json:"asset_kind"`
	AssetValue string `json:"asset_value"`
}
type Graph struct {
	Nodes   []Node   `json:"nodes"`
	Edges   []Edge   `json:"edges"`
	Anchors []Anchor `json:"anchors"`
}
type Coverage struct {
	AssetCount      int `json:"asset_count"`
	AnchoredAssets  int `json:"anchored_assets"`
	GoalCount       int `json:"goal_count"`
	ProvenGoalCount int `json:"proven_goal_count"`
}
type AssetInput struct {
	Kind         string          `json:"kind"`
	Value        string          `json:"value"`
	ParentID     *string         `json:"parent_id,omitempty"`
	Attributes   json.RawMessage `json:"attributes,omitempty"`
	Confidence   float32         `json:"confidence"`
	SourceTaskID *string         `json:"source_task_id,omitempty"`
}
type Asset struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organization_id"`
	Kind           string          `json:"kind"`
	CanonicalKey   string          `json:"canonical_key"`
	DisplayValue   string          `json:"display_value"`
	RawValue       string          `json:"raw_value"`
	ParentID       *string         `json:"parent_id,omitempty"`
	Attributes     json.RawMessage `json:"attributes"`
	SourceTaskID   *string         `json:"source_task_id,omitempty"`
	Confidence     float32         `json:"confidence"`
	FirstSeenAt    time.Time       `json:"first_seen_at"`
	LastSeenAt     time.Time       `json:"last_seen_at"`
}

type Store struct{ Pool *pgxpool.Pool }

const taskColumns = `id,organization_id,title,description,status,policy_mode,worker_limit,deadline_at,version,scope_version,created_by,created_at,updated_at`

func scanTask(row pgx.Row) (Task, error) {
	var result Task
	err := row.Scan(&result.ID, &result.OrganizationID, &result.Title, &result.Description, &result.Status,
		&result.PolicyMode, &result.WorkerLimit, &result.DeadlineAt, &result.Version,
		&result.ScopeVersion, &result.CreatedBy, &result.CreatedAt, &result.UpdatedAt)
	return result, err
}

func (s Store) Create(ctx context.Context, orgID, actorID string, input CreateInput) (Detail, error) {
	input.Title = strings.TrimSpace(input.Title)
	if n := utf8.RuneCountInString(input.Title); n < 1 || n > 200 || utf8.RuneCountInString(input.Description) > 20000 || len(input.Goals) < 1 || len(input.Goals) > 50 {
		return Detail{}, ErrInvalid
	}
	if len(input.LLMProfileChain) > 20 {
		return Detail{}, ErrInvalid
	}
	if input.Execution.PolicyMode == "" {
		input.Execution.PolicyMode = "autonomous"
	}
	if input.Execution.WorkerLimit == 0 {
		input.Execution.WorkerLimit = 3
	}
	if input.Execution.WorkerLimit < 1 || input.Execution.WorkerLimit > 50 || (input.Execution.PolicyMode != "autonomous" && input.Execution.PolicyMode != "graded" && input.Execution.PolicyMode != "approval") {
		return Detail{}, ErrInvalid
	}
	normalized, err := NormalizeScopeSet(input.Scope.Entries)
	if err != nil {
		return Detail{}, err
	}
	for _, goal := range input.Goals {
		if strings.TrimSpace(goal.Text) == "" || utf8.RuneCountInString(goal.Text) > 4000 || goal.Priority < 0 || goal.Priority > 10 {
			return Detail{}, ErrInvalid
		}
	}
	for _, constraint := range input.Constraints {
		if strings.TrimSpace(constraint.Text) == "" || utf8.RuneCountInString(constraint.Text) > 4000 || (constraint.Effect != "allow" && constraint.Effect != "deny") {
			return Detail{}, ErrInvalid
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Detail{}, err
	}
	defer tx.Rollback(ctx)
	id := identity.NewID()
	created, err := scanTask(tx.QueryRow(ctx, `INSERT INTO tasks(id,organization_id,title,description,policy_mode,worker_limit,deadline_at,created_by)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+taskColumns,
		id, orgID, input.Title, input.Description, input.Execution.PolicyMode, input.Execution.WorkerLimit,
		input.Execution.DeadlineAt, actorID))
	if err != nil {
		return Detail{}, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO task_budgets(task_id) VALUES($1)", id); err != nil {
		return Detail{}, err
	}
	originID := identity.NewID()
	if _, err := tx.Exec(ctx, `INSERT INTO exploration_nodes(id,task_id,kind,state,summary)
		VALUES($1,$2,'origin','confirmed',$3)`, originID, id, input.Description); err != nil {
		return Detail{}, err
	}
	for _, goal := range input.Goals {
		goalID := identity.NewID()
		if _, err := tx.Exec(ctx, `INSERT INTO task_goals(id,task_id,text,priority) VALUES($1,$2,$3,$4)`, goalID, id, strings.TrimSpace(goal.Text), goal.Priority); err != nil {
			return Detail{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO exploration_nodes(id,task_id,kind,state,summary) VALUES($1,$2,'goal','open',$3)`, goalID, id, strings.TrimSpace(goal.Text)); err != nil {
			return Detail{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO exploration_edges(task_id,from_node_id,to_node_id,kind) VALUES($1,$2,$3,'derived_from')`, id, originID, goalID); err != nil {
			return Detail{}, err
		}
	}
	for _, entry := range normalized {
		if _, err := tx.Exec(ctx, `INSERT INTO task_scopes(id,task_id,scope_version,effect,kind,value,normalized_value,ports,path_prefix,created_by)
			VALUES($1,$2,1,$3,$4,$5,$6,$7,$8,$9)`, identity.NewID(), id, entry.Effect, entry.Kind, entry.Value, entry.NormalizedValue, entry.Ports, entry.PathPrefix, actorID); err != nil {
			return Detail{}, err
		}
	}
	for _, constraint := range input.Constraints {
		if _, err := tx.Exec(ctx, `INSERT INTO task_constraints(id,task_id,effect,text,source) VALUES($1,$2,$3,$4,'user')`, identity.NewID(), id, constraint.Effect, strings.TrimSpace(constraint.Text)); err != nil {
			return Detail{}, err
		}
	}
	seenProfiles := map[string]bool{}
	for position, profileID := range input.LLMProfileChain {
		if seenProfiles[profileID] {
			return Detail{}, ErrInvalid
		}
		seenProfiles[profileID] = true
		var available bool
		if err := tx.QueryRow(ctx, `SELECT enabled AND credential_ref IS NOT NULL FROM llm_profiles WHERE id=$1 AND organization_id=$2`, profileID, orgID).Scan(&available); err != nil || !available {
			return Detail{}, ErrInvalid
		}
		if _, err := tx.Exec(ctx, `INSERT INTO task_llm_chain(task_id,position,profile_id) VALUES($1,$2,$3)`, id, position, profileID); err != nil {
			return Detail{}, err
		}
	}
	if err := emit(ctx, tx, orgID, id, "task.created", map[string]any{"task_id": id, "title": input.Title, "status": "draft"}); err != nil {
		return Detail{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id)
		VALUES($1,$2,$3,'task.created','task',$4)`, identity.NewID(), orgID, actorID, id); err != nil {
		return Detail{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Detail{}, err
	}
	return s.Get(ctx, orgID, created.ID)
}

func (s Store) Get(ctx context.Context, orgID, id string) (Detail, error) {
	result := Detail{Goals: []Goal{}, Constraints: []Constraint{}, Scope: []ScopeEntry{}}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Detail{}, err
	}
	defer tx.Rollback(ctx)
	result.Task, err = scanTask(tx.QueryRow(ctx, "SELECT "+taskColumns+" FROM tasks WHERE organization_id=$1 AND id=$2 AND deleted_at IS NULL", orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, ErrNotFound
	}
	if err != nil {
		return Detail{}, err
	}
	goals, err := tx.Query(ctx, `SELECT id,text,state,priority,version,created_at FROM task_goals WHERE task_id=$1 ORDER BY priority DESC,created_at,id`, id)
	if err != nil {
		return Detail{}, err
	}
	for goals.Next() {
		var goal Goal
		if err := goals.Scan(&goal.ID, &goal.Text, &goal.State, &goal.Priority, &goal.Version, &goal.CreatedAt); err != nil {
			goals.Close()
			return Detail{}, err
		}
		result.Goals = append(result.Goals, goal)
	}
	err = goals.Err()
	goals.Close()
	if err != nil {
		return Detail{}, err
	}
	constraints, err := tx.Query(ctx, `SELECT id,effect,text,source FROM task_constraints WHERE task_id=$1 ORDER BY created_at,id`, id)
	if err != nil {
		return Detail{}, err
	}
	for constraints.Next() {
		var item Constraint
		if err := constraints.Scan(&item.ID, &item.Effect, &item.Text, &item.Source); err != nil {
			constraints.Close()
			return Detail{}, err
		}
		result.Constraints = append(result.Constraints, item)
	}
	err = constraints.Err()
	constraints.Close()
	if err != nil {
		return Detail{}, err
	}
	scopes, err := tx.Query(ctx, `SELECT id,effect,kind,value,normalized_value,ports,path_prefix FROM task_scopes WHERE task_id=$1 AND scope_version=$2 ORDER BY created_at,id`, id, result.Task.ScopeVersion)
	if err != nil {
		return Detail{}, err
	}
	for scopes.Next() {
		var item ScopeEntry
		if err := scopes.Scan(&item.ID, &item.Effect, &item.Kind, &item.Value, &item.NormalizedValue, &item.Ports, &item.PathPrefix); err != nil {
			scopes.Close()
			return Detail{}, err
		}
		result.Scope = append(result.Scope, item)
	}
	err = scopes.Err()
	scopes.Close()
	if err != nil {
		return Detail{}, err
	}
	return result, tx.Commit(ctx)
}

func (s Store) List(ctx context.Context, orgID, cursor string, limit int) ([]Task, error) {
	if limit < 1 || limit > 100 {
		limit = 30
	}
	if cursor == "" {
		cursor = "ffffffff-ffff-7fff-bfff-ffffffffffff"
	}
	rows, err := s.Pool.Query(ctx, "SELECT "+taskColumns+" FROM tasks WHERE organization_id=$1 AND id<$2 AND deleted_at IS NULL ORDER BY id DESC LIMIT $3", orgID, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Task{}
	for rows.Next() {
		item, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s Store) Graph(ctx context.Context, orgID, taskID string) (Graph, error) {
	if _, err := s.GetTaskOnly(ctx, orgID, taskID); err != nil {
		return Graph{}, err
	}
	result := Graph{Nodes: []Node{}, Edges: []Edge{}, Anchors: []Anchor{}}
	rows, err := s.Pool.Query(ctx, `SELECT id,kind,state,summary,source_intent_id,run_id,tool_call_id,created_at
		FROM exploration_nodes WHERE task_id=$1 ORDER BY created_at,id LIMIT 10000`, taskID)
	if err != nil {
		return Graph{}, err
	}
	for rows.Next() {
		var item Node
		if err := rows.Scan(&item.ID, &item.Kind, &item.State, &item.Summary, &item.SourceIntentID, &item.RunID, &item.ToolCallID, &item.CreatedAt); err != nil {
			rows.Close()
			return Graph{}, err
		}
		result.Nodes = append(result.Nodes, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Graph{}, err
	}
	edges, err := s.Pool.Query(ctx, `SELECT from_node_id,to_node_id,kind FROM exploration_edges WHERE task_id=$1 LIMIT 20000`, taskID)
	if err != nil {
		return Graph{}, err
	}
	for edges.Next() {
		var item Edge
		if err := edges.Scan(&item.FromNodeID, &item.ToNodeID, &item.Kind); err != nil {
			edges.Close()
			return Graph{}, err
		}
		result.Edges = append(result.Edges, item)
	}
	err = edges.Err()
	edges.Close()
	if err != nil {
		return Graph{}, err
	}
	anchors, err := s.Pool.Query(ctx, `SELECT a.node_id,a.asset_id,s.kind,s.display_value FROM exploration_anchors a JOIN exploration_nodes n ON n.id=a.node_id JOIN assets s ON s.id=a.asset_id WHERE n.task_id=$1 LIMIT 20000`, taskID)
	if err != nil {
		return Graph{}, err
	}
	for anchors.Next() {
		var item Anchor
		if err := anchors.Scan(&item.NodeID, &item.AssetID, &item.AssetKind, &item.AssetValue); err != nil {
			anchors.Close()
			return Graph{}, err
		}
		result.Anchors = append(result.Anchors, item)
	}
	err = anchors.Err()
	anchors.Close()
	return result, err
}

func (s Store) GetTaskOnly(ctx context.Context, orgID, id string) (Task, error) {
	item, err := scanTask(s.Pool.QueryRow(ctx, "SELECT "+taskColumns+" FROM tasks WHERE organization_id=$1 AND id=$2 AND deleted_at IS NULL", orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return item, err
}

func (s Store) Coverage(ctx context.Context, orgID, taskID string) (Coverage, error) {
	if _, err := s.GetTaskOnly(ctx, orgID, taskID); err != nil {
		return Coverage{}, err
	}
	var result Coverage
	err := s.Pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM task_asset_links WHERE task_id=$1),
		(SELECT count(DISTINCT a.asset_id) FROM exploration_anchors a JOIN exploration_nodes n ON n.id=a.node_id WHERE n.task_id=$1),
		(SELECT count(*) FROM task_goals WHERE task_id=$1),
		(SELECT count(*) FROM task_goals WHERE task_id=$1 AND state='met')`, taskID).Scan(&result.AssetCount, &result.AnchoredAssets, &result.GoalCount, &result.ProvenGoalCount)
	return result, err
}

func emit(ctx context.Context, tx pgx.Tx, orgID, taskID, kind string, payload any) error {
	data, err := json.Marshal(map[string]any{"schema_version": 1, "data": payload})
	if err != nil {
		return err
	}
	var taskValue any
	if taskID != "" {
		taskValue = taskID
	}
	_, err = tx.Exec(ctx, `INSERT INTO domain_events(id,organization_id,task_id,event_type,payload) VALUES($1,$2,$3,$4,$5)`, identity.NewID(), orgID, taskValue, kind, data)
	return err
}
