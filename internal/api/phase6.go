package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/X9st/TianFaSRC/internal/tool"
	"github.com/jackc/pgx/v5"
)

type phase6API struct {
	phase1API
	dataDir string
}

func (api phase6API) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/dashboard", api.dashboard)
	mux.HandleFunc("GET /api/v1/audit-log", api.auditLog)
	mux.HandleFunc("GET /api/v1/policy", api.policyOverview)
	mux.HandleFunc("GET /api/v1/tasks/{id}/policy-blocks", api.policyBlocks)
	mux.HandleFunc("GET /api/v1/tasks/{id}/workspace", api.workspaceFiles)
	mux.HandleFunc("GET /api/v1/tasks/{id}/workspace/{intent}/{name}", api.workspaceFile)
	mux.HandleFunc("GET /api/v1/tasks/{id}/conversation", api.conversation)
	mux.HandleFunc("POST /api/v1/tasks/{id}/conversation", api.postConversation)
}

func (api phase6API) dashboard(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	var activeWorkers int
	var inputTokens, outputTokens, costMinor int64
	if err := api.tasks.Pool.QueryRow(r.Context(), `SELECT count(*) FROM worker_leases l JOIN tasks t ON t.id=l.task_id WHERE t.organization_id=$1 AND l.expires_at>now()`, user.OrganizationID).Scan(&activeWorkers); err != nil {
		apiError(w, r, err)
		return
	}
	if err := api.tasks.Pool.QueryRow(r.Context(), `SELECT coalesce(sum(b.input_tokens),0),coalesce(sum(b.output_tokens),0),coalesce(sum(b.cost_minor),0) FROM task_budgets b JOIN tasks t ON t.id=b.task_id WHERE t.organization_id=$1`, user.OrganizationID).Scan(&inputTokens, &outputTokens, &costMinor); err != nil {
		apiError(w, r, err)
		return
	}
	findings := map[string]int{}
	findingStatuses := map[string]int{}
	rows, err := api.tasks.Pool.Query(r.Context(), `SELECT severity,count(*) FROM findings WHERE organization_id=$1 GROUP BY severity`, user.OrganizationID)
	if err != nil {
		apiError(w, r, err)
		return
	}
	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			rows.Close()
			apiError(w, r, err)
			return
		}
		findings[key] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		apiError(w, r, err)
		return
	}
	rows, err = api.tasks.Pool.Query(r.Context(), `SELECT status,count(*) FROM findings WHERE organization_id=$1 GROUP BY status`, user.OrganizationID)
	if err != nil {
		apiError(w, r, err)
		return
	}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			apiError(w, r, err)
			return
		}
		findingStatuses[status] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		apiError(w, r, err)
		return
	}
	type reviewFinding struct {
		ID        string `json:"id"`
		TaskID    string `json:"task_id"`
		TaskTitle string `json:"task_title"`
		Title     string `json:"title"`
		Severity  string `json:"severity"`
	}
	reviewQueue := []reviewFinding{}
	rows, err = api.tasks.Pool.Query(r.Context(), "SELECT f.id,f.task_id,t.title,f.title,f.severity FROM findings f JOIN tasks t ON t.id=f.task_id AND t.organization_id=f.organization_id WHERE f.organization_id=$1 AND f.status='draft' ORDER BY CASE f.severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END,f.created_at DESC,f.id DESC LIMIT 5", user.OrganizationID)
	if err != nil {
		apiError(w, r, err)
		return
	}
	for rows.Next() {
		var item reviewFinding
		if err := rows.Scan(&item.ID, &item.TaskID, &item.TaskTitle, &item.Title, &item.Severity); err != nil {
			rows.Close()
			apiError(w, r, err)
			return
		}
		reviewQueue = append(reviewQueue, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		apiError(w, r, err)
		return
	}
	models := map[string]int{}
	rows, err = api.tasks.Pool.Query(r.Context(), `SELECT health_state,count(*) FROM llm_profiles WHERE organization_id=$1 GROUP BY health_state`, user.OrganizationID)
	if err != nil {
		apiError(w, r, err)
		return
	}
	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			rows.Close()
			apiError(w, r, err)
			return
		}
		models[key] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		apiError(w, r, err)
		return
	}
	type activity struct {
		TaskID     string    `json:"task_id"`
		Kind       string    `json:"kind"`
		Summary    string    `json:"summary"`
		OccurredAt time.Time `json:"occurred_at"`
	}
	recent := []activity{}
	rows, err = api.tasks.Pool.Query(r.Context(), `SELECT a.task_id,a.kind,a.summary,a.occurred_at FROM activities a JOIN tasks t ON t.id=a.task_id WHERE t.organization_id=$1 ORDER BY a.occurred_at DESC,a.id DESC LIMIT 12`, user.OrganizationID)
	if err != nil {
		apiError(w, r, err)
		return
	}
	for rows.Next() {
		var x activity
		if err := rows.Scan(&x.TaskID, &x.Kind, &x.Summary, &x.OccurredAt); err != nil {
			rows.Close()
			apiError(w, r, err)
			return
		}
		recent = append(recent, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, map[string]any{"active_workers": activeWorkers, "input_tokens": inputTokens, "output_tokens": outputTokens, "cost_minor": costMinor, "findings": findings, "finding_statuses": findingStatuses, "review_queue": reviewQueue, "models": models, "recent_activity": recent})
}

func (api phase6API) auditLog(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	limit := listLimit(r)
	if limit > 100 {
		limit = 100
	}
	cursor := r.URL.Query().Get("cursor")
	var at time.Time
	var cursorID string
	if cursor != "" {
		if err := api.tasks.Pool.QueryRow(r.Context(), `SELECT occurred_at,id FROM audit_log WHERE id=$1 AND organization_id=$2`, cursor, user.OrganizationID).Scan(&at, &cursorID); err != nil {
			apiError(w, r, task.ErrInvalid)
			return
		}
	} else {
		at = time.Now().Add(time.Hour)
	}
	rows, err := api.tasks.Pool.Query(r.Context(), `SELECT a.id,a.action,a.resource_type,a.resource_id,coalesce(u.email,''),a.occurred_at FROM audit_log a LEFT JOIN users u ON u.id=a.actor_user_id WHERE a.organization_id=$1 AND (a.occurred_at,a.id)<($2,$3::uuid) ORDER BY a.occurred_at DESC,a.id DESC LIMIT $4`, user.OrganizationID, at, coalesceUUID(cursorID), limit)
	if err != nil {
		apiError(w, r, err)
		return
	}
	defer rows.Close()
	type item struct {
		ID           string    `json:"id"`
		Action       string    `json:"action"`
		ResourceType string    `json:"resource_type"`
		ResourceID   *string   `json:"resource_id"`
		Actor        string    `json:"actor"`
		OccurredAt   time.Time `json:"occurred_at"`
	}
	items := []item{}
	for rows.Next() {
		var x item
		if err := rows.Scan(&x.ID, &x.Action, &x.ResourceType, &x.ResourceID, &x.Actor, &x.OccurredAt); err != nil {
			apiError(w, r, err)
			return
		}
		items = append(items, x)
	}
	if err := rows.Err(); err != nil {
		apiError(w, r, err)
		return
	}
	next := ""
	if len(items) == limit {
		next = items[len(items)-1].ID
	}
	writeData(w, r, 200, map[string]any{"items": items, "next_cursor": next})
}
func (api phase6API) policyOverview(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	var workers, blocked int
	if err := api.tasks.Pool.QueryRow(r.Context(), `SELECT max_workers FROM scheduler_capacity WHERE singleton=true`).Scan(&workers); err != nil {
		apiError(w, r, err)
		return
	}
	if err := api.tasks.Pool.QueryRow(r.Context(), `SELECT count(*) FROM tool_calls WHERE organization_id=$1 AND status='blocked' AND policy_reason IS NOT NULL`, user.OrganizationID).Scan(&blocked); err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, map[string]any{"global_worker_limit": workers, "blocked_tool_calls": blocked, "worker_hard_limits": map[string]int{"model_rounds": 8, "tool_calls": 12, "tokens": 30000, "seconds": 180}})
}
func (api phase6API) policyBlocks(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if _, err := api.tasks.Get(r.Context(), user.OrganizationID, id); err != nil {
		apiError(w, r, err)
		return
	}
	rows, err := api.tasks.Pool.Query(r.Context(), `SELECT id,intent_id,name,coalesce(policy_reason,''),coalesce(summary,''),created_at FROM tool_calls WHERE organization_id=$1 AND task_id=$2 AND status='blocked' AND policy_reason IS NOT NULL ORDER BY created_at DESC,id DESC LIMIT 100`, user.OrganizationID, id)
	if err != nil {
		apiError(w, r, err)
		return
	}
	defer rows.Close()
	type item struct {
		ID        string    `json:"id"`
		IntentID  string    `json:"intent_id"`
		Tool      string    `json:"tool"`
		Reason    string    `json:"reason"`
		Summary   string    `json:"summary"`
		CreatedAt time.Time `json:"created_at"`
	}
	items := []item{}
	for rows.Next() {
		var x item
		if err := rows.Scan(&x.ID, &x.IntentID, &x.Tool, &x.Reason, &x.Summary, &x.CreatedAt); err != nil {
			apiError(w, r, err)
			return
		}
		items = append(items, x)
	}
	if err := rows.Err(); err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}
func (api phase6API) workspaceFiles(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	id := r.PathValue("id")
	if _, err := api.tasks.Get(r.Context(), user.OrganizationID, id); err != nil {
		apiError(w, r, err)
		return
	}
	items, err := (tool.Workspace{Root: api.dataDir}).ListTaskFiles(id)
	if err != nil {
		apiError(w, r, err)
		return
	}
	rows, err := api.tasks.Pool.Query(r.Context(), `SELECT id FROM task_intents WHERE task_id=$1`, id)
	if err != nil {
		apiError(w, r, err)
		return
	}
	defer rows.Close()
	allowed := map[string]bool{}
	for rows.Next() {
		var intent string
		if err := rows.Scan(&intent); err != nil {
			apiError(w, r, err)
			return
		}
		allowed[intent] = true
	}
	if err := rows.Err(); err != nil {
		apiError(w, r, err)
		return
	}
	filtered := []tool.WorkspaceFile{}
	for _, item := range items {
		if allowed[item.IntentID] {
			filtered = append(filtered, item)
		}
	}
	writeData(w, r, 200, filtered)
}
func (api phase6API) workspaceFile(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	id, intent, name := r.PathValue("id"), r.PathValue("intent"), r.PathValue("name")
	if _, err := api.tasks.Get(r.Context(), user.OrganizationID, id); err != nil {
		apiError(w, r, err)
		return
	}
	var exists bool
	if err := api.tasks.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM task_intents WHERE id=$1 AND task_id=$2)`, intent, id).Scan(&exists); err != nil {
		apiError(w, r, err)
		return
	}
	if !exists {
		apiError(w, r, task.ErrNotFound)
		return
	}
	content, err := (tool.Workspace{Root: api.dataDir}).ReadTaskFile(id, intent, name)
	if errors.Is(err, os.ErrNotExist) {
		apiError(w, r, task.ErrNotFound)
		return
	}
	if err != nil {
		apiError(w, r, task.ErrInvalid)
		return
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
		w.WriteHeader(200)
		_, _ = w.Write(content)
		return
	}
	preview := ""
	if utf8.Valid(content) && !strings.ContainsRune(string(content), 0) {
		preview = string(content)
		if len(preview) > 64<<10 {
			preview = preview[:64<<10]
		}
	}
	writeData(w, r, 200, map[string]any{"intent_id": intent, "name": name, "size": len(content), "text_preview": preview, "preview_available": preview != ""})
}

type conversationItem struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Action    string    `json:"action"`
	Content   string    `json:"content"`
	RunID     *string   `json:"run_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func (api phase6API) conversation(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if _, err := api.tasks.Get(r.Context(), user.OrganizationID, id); err != nil {
		apiError(w, r, err)
		return
	}
	rows, err := api.tasks.Pool.Query(r.Context(), `SELECT id,role,action,content,run_id,created_at FROM (SELECT c.id,c.role,c.action,c.content,NULL::uuid AS run_id,c.created_at FROM task_conversation c WHERE c.task_id=$1 AND c.organization_id=$2 UNION ALL SELECT m.id,r.role,'agent'::text,left(m.content,4000),r.id,m.created_at FROM agent_messages m JOIN agent_runs r ON r.id=m.run_id WHERE r.task_id=$1 AND r.organization_id=$2 AND m.role='assistant' ORDER BY created_at DESC,id DESC LIMIT 100) recent ORDER BY created_at,id`, id, user.OrganizationID)
	if err != nil {
		apiError(w, r, err)
		return
	}
	defer rows.Close()
	items := []conversationItem{}
	for rows.Next() {
		var x conversationItem
		if err := rows.Scan(&x.ID, &x.Role, &x.Action, &x.Content, &x.RunID, &x.CreatedAt); err != nil {
			apiError(w, r, err)
			return
		}
		items = append(items, x)
	}
	if err := rows.Err(); err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}
func phase6Event(ctx context.Context, tx pgx.Tx, orgID, taskID, kind string, data map[string]any) error {
	_, err := tx.Exec(ctx, `INSERT INTO domain_events(id,organization_id,task_id,event_type,payload) VALUES($1,$2,$3,$4,$5)`, identity.NewID(), orgID, taskID, kind, map[string]any{"schema_version": 1, "data": data})
	return err
}

func (api phase6API) postConversation(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	var input struct {
		Action          string `json:"action"`
		Text            string `json:"text"`
		GoalID          string `json:"goal_id"`
		IntentID        string `json:"intent_id"`
		SuccessCriteria string `json:"success_criteria"`
	}
	if err := decodeJSON(r, &input); err != nil {
		apiError(w, r, task.ErrInvalid)
		return
	}
	input.Text = strings.TrimSpace(input.Text)
	input.SuccessCriteria = strings.TrimSpace(input.SuccessCriteria)
	if input.Text == "" || len(input.Text) > 4000 {
		apiError(w, r, task.ErrInvalid)
		return
	}
	switch input.Action {
	case "ask", "goal", "constraint", "intent", "steer":
	default:
		apiError(w, r, task.ErrInvalid)
		return
	}
	if input.Action == "intent" && (input.SuccessCriteria == "" || len(input.SuccessCriteria) > 4000) {
		apiError(w, r, task.ErrInvalid)
		return
	}
	id := r.PathValue("id")
	tx, err := api.tasks.Pool.Begin(r.Context())
	if err != nil {
		apiError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var status string
	err = tx.QueryRow(r.Context(), `SELECT status FROM tasks WHERE id=$1 AND organization_id=$2 AND deleted_at IS NULL FOR UPDATE`, id, user.OrganizationID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(w, r, task.ErrNotFound)
		return
	}
	if err != nil {
		apiError(w, r, err)
		return
	}
	reply := ""
	switch input.Action {
	case "ask":
		var goals, intents, findings, workers int
		for _, q := range []struct {
			sql  string
			dest *int
		}{
			{`SELECT count(*) FROM task_goals WHERE task_id=$1 AND state='open'`, &goals},
			{`SELECT count(*) FROM task_intents WHERE task_id=$1 AND status IN ('open','leased','running')`, &intents},
			{`SELECT count(*) FROM findings WHERE task_id=$1`, &findings},
			{`SELECT count(*) FROM worker_leases WHERE task_id=$1 AND expires_at>now()`, &workers},
		} {
			if err = tx.QueryRow(r.Context(), q.sql, id).Scan(q.dest); err != nil {
				apiError(w, r, err)
				return
			}
		}
		reply = fmt.Sprintf("当前任务状态：%s；未证明目标 %d 个、待执行或运行中的方向 %d 个、活跃 Worker %d 个、漏洞记录 %d 条。详情以任务页的证据和运行记录为准。", status, goals, intents, workers, findings)
	case "goal":
		if status == "cancelled" || status == "failed" {
			apiError(w, r, task.ErrConflict)
			return
		}
		var count int
		if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM task_goals WHERE task_id=$1`, id).Scan(&count); err != nil {
			apiError(w, r, err)
			return
		}
		if count >= 50 {
			apiError(w, r, task.ErrInvalid)
			return
		}
		var origin string
		if err = tx.QueryRow(r.Context(), `SELECT id FROM exploration_nodes WHERE task_id=$1 AND kind='origin'`, id).Scan(&origin); err != nil {
			apiError(w, r, err)
			return
		}
		goal := identity.NewID()
		if _, err = tx.Exec(r.Context(), `INSERT INTO task_goals(id,task_id,text,priority) VALUES($1,$2,$3,9)`, goal, id, input.Text); err != nil {
			apiError(w, r, err)
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO exploration_nodes(id,task_id,kind,state,summary) VALUES($1,$2,'goal','open',$3)`, goal, id, input.Text); err != nil {
			apiError(w, r, err)
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO exploration_edges(task_id,from_node_id,to_node_id,kind) VALUES($1,$2,$3,'derived_from')`, id, origin, goal); err != nil {
			apiError(w, r, err)
			return
		}
		if _, err = tx.Exec(r.Context(), `UPDATE tasks SET status=CASE WHEN status='completed' THEN 'queued' ELSE status END,version=version+1,updated_at=now() WHERE id=$1`, id); err != nil {
			apiError(w, r, err)
			return
		}
		if status == "running" || status == "queued" || status == "completed" {
			if _, err = tx.Exec(r.Context(), `INSERT INTO planner_wakes(task_id,due_at,reason) VALUES($1,now(),'human.goal') ON CONFLICT(task_id) DO UPDATE SET pending=true,due_at=now(),reason='human.goal',updated_at=now()`, id); err != nil {
				apiError(w, r, err)
				return
			}
		}
		if err = phase6Event(r.Context(), tx, user.OrganizationID, id, "goal.added", map[string]any{"goal_id": goal}); err != nil {
			apiError(w, r, err)
			return
		}
		reply = "高优先级目标已加入任务，并连接到探索图。"
	case "constraint":
		if status != "draft" && status != "paused" {
			writeProblem(w, r, 409, "task-not-paused", "请先暂停任务", "新增禁止事项会改变 Worker 的执行边界；请暂停任务后再提交")
			return
		}
		cid := identity.NewID()
		if _, err = tx.Exec(r.Context(), `INSERT INTO task_constraints(id,task_id,effect,text,source) VALUES($1,$2,'deny',$3,'user')`, cid, id, input.Text); err != nil {
			apiError(w, r, err)
			return
		}
		if _, err = tx.Exec(r.Context(), `UPDATE tasks SET version=version+1,updated_at=now() WHERE id=$1`, id); err != nil {
			apiError(w, r, err)
			return
		}
		if err = phase6Event(r.Context(), tx, user.OrganizationID, id, "constraint.added", map[string]any{"constraint_id": cid}); err != nil {
			apiError(w, r, err)
			return
		}
		reply = "禁止事项已加入任务。恢复前请检查 Scope 与策略。"
	case "intent":
		if status != "running" && status != "queued" && status != "paused" {
			apiError(w, r, task.ErrConflict)
			return
		}
		var goal bool
		if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM task_goals WHERE id=$1 AND task_id=$2 AND state='open')`, input.GoalID, id).Scan(&goal); err != nil {
			apiError(w, r, err)
			return
		}
		if !goal {
			apiError(w, r, task.ErrInvalid)
			return
		}
		intent := identity.NewID()
		dedup := sha256.Sum256([]byte(input.GoalID + ":" + strings.ToLower(input.Text)))
		if _, err = tx.Exec(r.Context(), `INSERT INTO exploration_nodes(id,task_id,kind,state,summary) VALUES($1,$2,'intent','open',$3)`, intent, id, input.Text); err != nil {
			apiError(w, r, err)
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO task_intents(id,task_id,goal_id,objective,success_criteria,dedup_key,priority,budget_model_rounds,budget_tool_calls,budget_tokens,budget_seconds,budget_reason) VALUES($1,$2,$3,$4,$5,$6,9,5,6,16000,120,'高优先级方向的系统默认值')`, intent, id, input.GoalID, input.Text, input.SuccessCriteria, hex.EncodeToString(dedup[:])); err != nil {
			apiError(w, r, err)
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO exploration_edges(task_id,from_node_id,to_node_id,kind) VALUES($1,$2,$3,'spawns')`, id, input.GoalID, intent); err != nil {
			apiError(w, r, err)
			return
		}
		if err = phase6Event(r.Context(), tx, user.OrganizationID, id, "intent.created", map[string]any{"intent_id": intent, "source": "human"}); err != nil {
			apiError(w, r, err)
			return
		}
		reply = "高优先级验证方向已入队；Worker 将在任务运行时领取。"
	case "steer":
		var active bool
		if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM task_intents WHERE id=$1 AND task_id=$2 AND status IN ('open','leased','running','blocked'))`, input.IntentID, id).Scan(&active); err != nil {
			apiError(w, r, err)
			return
		}
		if !active {
			apiError(w, r, task.ErrConflict)
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO worker_steers(id,task_id,intent_id,actor_user_id,message) VALUES($1,$2,$3,$4,$5)`, identity.NewID(), id, input.IntentID, user.ID, input.Text); err != nil {
			apiError(w, r, err)
			return
		}
		if err = phase6Event(r.Context(), tx, user.OrganizationID, id, "hint.steer", map[string]any{"intent_id": input.IntentID}); err != nil {
			apiError(w, r, err)
			return
		}
		reply = "纠偏指令已保存，将在 Worker 下次模型动作前加入上下文。"
	}
	now := time.Now()
	humanID, systemID := identity.NewID(), identity.NewID()
	if _, err = tx.Exec(r.Context(), `INSERT INTO task_conversation(id,organization_id,task_id,actor_user_id,role,action,content,created_at) VALUES($1,$2,$3,$4,'human',$5,$6,$7),($8,$2,$3,NULL,'system',$5,$9,$10)`, humanID, user.OrganizationID, id, user.ID, input.Action, input.Text, now, systemID, reply, now.Add(time.Microsecond)); err != nil {
		apiError(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id) VALUES($1,$2,$3,$4,'task',$5)`, identity.NewID(), user.OrganizationID, user.ID, "conversation."+input.Action, id); err != nil {
		apiError(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 201, map[string]any{"reply": reply, "message_id": systemID})
}
