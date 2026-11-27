package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/llm"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/X9st/TianFaSRC/internal/tool"
	"github.com/jackc/pgx/v5"
)

const leaseDuration = 30 * time.Second

// IdempotentObservationSource must use key for every external side effect.
// An implementation must return the original result on a repeated key.
type IdempotentObservationSource interface {
	ObserveOnce(context.Context, string, string, string) (Observation, error)
}

type Scheduler struct {
	Engine       Engine
	Observations IdempotentObservationSource
	Tools        *tool.Dispatcher
	Logger       *slog.Logger
	OwnerID      string
}

type Claim struct {
	IntentID, TaskID, OrganizationID, ActorID, RunID string
	Objective, SuccessCriteria                       string
	Attempt                                          int
	Fence                                            int64
	ObservationGeneration                            int
	SteerIDs                                         []string
	PlannerSince                                     *time.Time
	PlannerClaimedAt                                 time.Time
}

func (s Scheduler) owner() string {
	if s.OwnerID != "" {
		return s.OwnerID
	}
	return identity.NewID()
}

// Start records admission and a durable planner wake in one transaction.
func (s Scheduler) Start(ctx context.Context, orgID, actorID, taskID string, version int64) error {
	detail, err := (task.Store{Pool: s.Engine.Pool}).Get(ctx, orgID, taskID)
	if err != nil {
		return err
	}
	if detail.Task.Version != version || detail.Task.Status != "draft" || len(detail.Scope) == 0 {
		return task.ErrConflict
	}
	allowed := false
	for _, entry := range detail.Scope {
		allowed = allowed || entry.Effect == "allow"
	}
	if !allowed {
		return task.ErrInvalid
	}
	profiles, err := s.Engine.Profiles.ResolvedChain(ctx, orgID, taskID, "planner")
	if err != nil {
		return err
	}
	if len(profiles) == 0 {
		return task.ErrModelUnavailable
	}
	if _, err = s.Engine.Profiles.ActivePrompt(ctx, orgID, actorID, "worker"); err != nil {
		return err
	}
	tx, err := s.Engine.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE tasks SET status='queued',version=version+1,updated_at=now() WHERE id=$1 AND organization_id=$2 AND status='draft' AND version=$3`, taskID, orgID, version)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return task.ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO planner_wakes(task_id,due_at,reason) VALUES($1,now(),'task.started')`, taskID); err != nil {
		return err
	}
	if err = event(ctx, tx, orgID, taskID, "task.started", map[string]any{"task_id": taskID}); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id) VALUES($1,$2,$3,'task.started','task',$4)`, identity.NewID(), orgID, actorID, taskID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s Scheduler) Wake(ctx context.Context, orgID, taskID, reason string) error {
	tag, err := s.Engine.Pool.Exec(ctx, `INSERT INTO planner_wakes(task_id,due_at,reason) SELECT id,now()+interval '500 milliseconds',$3 FROM tasks WHERE id=$1 AND organization_id=$2 AND status IN ('queued','running') ON CONFLICT(task_id) DO UPDATE SET pending=true,due_at=LEAST(planner_wakes.due_at,excluded.due_at),reason=excluded.reason,updated_at=now()`, taskID, orgID, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return task.ErrConflict
	}
	return nil
}

// PumpEvents consumes the outbox once per event and coalesces wakeups.
func (s Scheduler) PumpEvents(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	tx, err := s.Engine.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT e.id,e.task_id,e.event_type FROM domain_events e WHERE e.task_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM event_consumptions c WHERE c.consumer_name='planner' AND c.event_id=e.id) ORDER BY e.occurred_at,e.id LIMIT $1 FOR UPDATE OF e SKIP LOCKED`, limit)
	if err != nil {
		return 0, err
	}
	type pending struct{ id, taskID, kind string }
	items := []pending{}
	for rows.Next() {
		var item pending
		if err := rows.Scan(&item.id, &item.taskID, &item.kind); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		if _, err = tx.Exec(ctx, `INSERT INTO event_consumptions(consumer_name,event_id) VALUES('planner',$1) ON CONFLICT DO NOTHING`, item.id); err != nil {
			return 0, err
		}
		if !plannerTrigger(item.kind) {
			continue
		}
		if _, err = tx.Exec(ctx, `INSERT INTO planner_wakes(task_id,due_at,reason) SELECT id,now()+interval '500 milliseconds',$2 FROM tasks WHERE id=$1 AND status IN ('queued','running') ON CONFLICT(task_id) DO UPDATE SET pending=true,due_at=LEAST(planner_wakes.due_at,excluded.due_at),reason=excluded.reason,updated_at=now()`, item.taskID, item.kind); err != nil {
			return 0, err
		}
	}
	return len(items), tx.Commit(ctx)
}

func plannerTrigger(kind string) bool {
	return strings.HasPrefix(kind, "goal.") || strings.HasPrefix(kind, "fact.") || strings.HasPrefix(kind, "finding.") || strings.HasPrefix(kind, "hint.") || kind == "todo.done" || strings.HasPrefix(kind, "intent.done") || strings.HasPrefix(kind, "intent.blocked") || kind == "intent.stopped" || kind == "intent.exhausted" || kind == "intent.reconciled" || kind == "task.resumed" || kind == "task.started"
}

func (s Scheduler) ClaimPlanner(ctx context.Context) (Claim, bool, error) {
	tx, err := s.Engine.Pool.Begin(ctx)
	if err != nil {
		return Claim{}, false, err
	}
	defer tx.Rollback(ctx)
	var claim Claim
	err = tx.QueryRow(ctx, `SELECT w.task_id,t.organization_id,t.created_by,w.fence+1,w.last_planned_at,now() FROM planner_wakes w JOIN tasks t ON t.id=w.task_id WHERE t.status IN ('queued','running') AND w.pending AND w.due_at<=now() AND (w.expires_at IS NULL OR w.expires_at<now()) ORDER BY w.due_at,w.task_id LIMIT 1 FOR UPDATE OF w SKIP LOCKED`).Scan(&claim.TaskID, &claim.OrganizationID, &claim.ActorID, &claim.Fence, &claim.PlannerSince, &claim.PlannerClaimedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Claim{}, false, nil
	}
	if err != nil {
		return Claim{}, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE planner_wakes SET pending=false,owner_id=$2,fence=$3,expires_at=now()+interval '30 seconds',last_planned_at=$4,updated_at=now() WHERE task_id=$1`, claim.TaskID, s.owner(), claim.Fence, claim.PlannerClaimedAt)
	if err != nil {
		return Claim{}, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE tasks SET status='running',version=version+1,updated_at=now() WHERE id=$1 AND status='queued'`, claim.TaskID)
	if err != nil {
		return Claim{}, false, err
	}
	return claim, true, tx.Commit(ctx)
}

func (s Scheduler) ClaimWorker(ctx context.Context) (Claim, bool, error) {
	tx, err := s.Engine.Pool.Begin(ctx)
	if err != nil {
		return Claim{}, false, err
	}
	defer tx.Rollback(ctx)
	var globalLimit, activeWorkers int
	if err = tx.QueryRow(ctx, `SELECT max_workers FROM scheduler_capacity WHERE singleton=true FOR UPDATE`).Scan(&globalLimit); err != nil {
		return Claim{}, false, err
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM worker_leases WHERE expires_at>now()`).Scan(&activeWorkers); err != nil {
		return Claim{}, false, err
	}
	if activeWorkers >= globalLimit {
		return Claim{}, false, nil
	}
	var c Claim
	err = tx.QueryRow(ctx, `SELECT i.id,i.task_id,t.organization_id,t.created_by,i.objective,i.success_criteria,i.attempt+1,i.observation_generation
 FROM task_intents i JOIN tasks t ON t.id=i.task_id
 WHERE i.status='open' AND i.ready_at<=now() AND t.status='running' AND i.attempt<i.max_attempts
 AND (i.todo_id IS NULL OR EXISTS(SELECT 1 FROM task_todos d WHERE d.id=i.todo_id AND d.status='open'
   AND NOT EXISTS(SELECT 1 FROM unnest(d.depends_on) dep JOIN task_todos p ON p.id=dep WHERE p.status<>'done')))
 AND (t.deadline_at IS NULL OR t.deadline_at>now())
 AND (SELECT count(*) FROM worker_leases l WHERE l.task_id=t.id AND l.expires_at>now())<t.worker_limit
 ORDER BY COALESCE(t.last_worker_dispatch_at,t.created_at),i.priority DESC,i.created_at,i.id
 LIMIT 1 FOR UPDATE OF i,t SKIP LOCKED`).Scan(&c.IntentID, &c.TaskID, &c.OrganizationID, &c.ActorID, &c.Objective, &c.SuccessCriteria, &c.Attempt, &c.ObservationGeneration)
	if errors.Is(err, pgx.ErrNoRows) {
		return Claim{}, false, nil
	}
	if err != nil {
		return Claim{}, false, err
	}
	c.RunID = identity.NewID()
	c.Fence = int64(c.Attempt)
	var promptVersion int
	var promptHash string
	err = tx.QueryRow(ctx, `SELECT p.version,p.content_hash FROM active_prompt_templates a JOIN prompt_templates p ON p.organization_id=a.organization_id AND p.role=a.role AND p.version=a.version WHERE a.organization_id=$1 AND a.role='worker'`, c.OrganizationID).Scan(&promptVersion, &promptHash)
	if errors.Is(err, pgx.ErrNoRows) {
		prompt, createErr := s.Engine.Profiles.ActivePrompt(ctx, c.OrganizationID, c.ActorID, "worker")
		if createErr != nil {
			return Claim{}, false, createErr
		}
		promptVersion, promptHash, err = prompt.Version, prompt.ContentHash, nil
	}
	if err != nil {
		return Claim{}, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO agent_runs(id,organization_id,task_id,intent_id,role,status,prompt_version,prompt_hash,session_key,attempt) VALUES($1,$2,$3,$4,'worker','running',$5,$6,$7,$8)`, c.RunID, c.OrganizationID, c.TaskID, c.IntentID, promptVersion, promptHash, fmt.Sprintf("task/%s/intent/%s/attempt/%d", c.TaskID, c.IntentID, c.Attempt), c.Attempt)
	if err != nil {
		return Claim{}, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO worker_run_budgets(run_id,organization_id,task_id,intent_id,max_model_rounds,max_tool_calls,max_tokens,max_seconds,reason) SELECT $1,$2,$3,$4,budget_model_rounds,budget_tool_calls,budget_tokens,budget_seconds,budget_reason FROM task_intents WHERE id=$4`, c.RunID, c.OrganizationID, c.TaskID, c.IntentID)
	if err != nil {
		return Claim{}, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE task_intents SET status='leased',attempt=$2,last_dispatched_at=now(),updated_at=now(),blocking_reason=NULL WHERE id=$1`, c.IntentID, c.Attempt)
	if err != nil {
		return Claim{}, false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE tasks SET last_worker_dispatch_at=now() WHERE id=$1`, c.TaskID); err != nil {
		return Claim{}, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO worker_leases(intent_id,task_id,run_id,owner_id,fence,attempt,expires_at) VALUES($1,$2,$3,$4,$5,$6,now()+interval '30 seconds')`, c.IntentID, c.TaskID, c.RunID, s.owner(), c.Fence, c.Attempt)
	if err != nil {
		return Claim{}, false, err
	}
	if err = event(ctx, tx, c.OrganizationID, c.TaskID, "intent.leased", map[string]any{"intent_id": c.IntentID, "run_id": c.RunID, "attempt": c.Attempt}); err != nil {
		return Claim{}, false, err
	}
	return c, true, tx.Commit(ctx)
}

func (s Scheduler) Renew(ctx context.Context, c Claim) (bool, error) {
	tag, err := s.Engine.Pool.Exec(ctx, `UPDATE worker_leases SET heartbeat_at=now(),expires_at=now()+interval '30 seconds' WHERE intent_id=$1 AND run_id=$2 AND fence=$3 AND expires_at>now()`, c.IntentID, c.RunID, c.Fence)
	return tag.RowsAffected() == 1, err
}

func (s Scheduler) RunWorker(ctx context.Context, c Claim) error {
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan struct{})
	go func() {
		ticker := time.NewTicker(leaseDuration / 3)
		defer ticker.Stop()
		defer close(finished)
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				ok, err := s.Renew(workerCtx, c)
				if err != nil || !ok {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-finished }()
	startTx, err := s.Engine.Pool.Begin(workerCtx)
	if err != nil {
		return err
	}
	tag, err := startTx.Exec(workerCtx, `UPDATE task_intents SET status='running',updated_at=now() WHERE id=$1 AND status='leased' AND EXISTS(SELECT 1 FROM worker_leases WHERE intent_id=$1 AND run_id=$2 AND fence=$3 AND expires_at>now())`, c.IntentID, c.RunID, c.Fence)
	if err != nil {
		_ = startTx.Rollback(workerCtx)
		return err
	}
	if tag.RowsAffected() != 1 {
		_ = startTx.Rollback(workerCtx)
		return task.ErrConflict
	}
	if err = event(workerCtx, startTx, c.OrganizationID, c.TaskID, "intent.running", map[string]any{"intent_id": c.IntentID, "run_id": c.RunID}); err != nil {
		_ = startTx.Rollback(workerCtx)
		return err
	}
	if err = startTx.Commit(workerCtx); err != nil {
		return err
	}
	var taskStatus string
	if err := s.Engine.Pool.QueryRow(workerCtx, `SELECT status FROM tasks WHERE id=$1`, c.TaskID).Scan(&taskStatus); err != nil {
		return err
	}
	if taskStatus == "paused" || taskStatus == "cancelled" {
		return s.FinishWorker(workerCtx, c, "paused", "任务已暂停", nil)
	}
	if s.Observations == nil {
		if s.Tools != nil {
			return s.runWorkerTools(workerCtx, c)
		}
		return s.FinishWorker(workerCtx, c, "blocked", "等待受控工具配置后验证", nil)
	}
	obs, err := s.observe(workerCtx, c)
	if err != nil {
		return s.FinishWorker(workerCtx, c, "blocked", err.Error(), nil)
	}
	if !obs.Confirmed || obs.ToolCallID == "" || strings.TrimSpace(obs.Summary) == "" {
		return s.FinishWorker(workerCtx, c, "blocked", "真实工具未返回可验证的结果", nil)
	}
	if err = s.Engine.Pool.QueryRow(workerCtx, `SELECT status FROM tasks WHERE id=$1`, c.TaskID).Scan(&taskStatus); err != nil {
		return err
	}
	if taskStatus == "paused" || taskStatus == "cancelled" {
		return s.FinishWorker(workerCtx, c, "paused", "任务已暂停", nil)
	}
	if reason, err := s.deadlineReached(workerCtx, c.TaskID); err != nil {
		return err
	} else if reason != "" {
		return s.FinishWorker(workerCtx, c, "exhausted", reason, &obs)
	}
	if ok, err := s.Renew(workerCtx, c); err != nil {
		return err
	} else if !ok {
		return task.ErrConflict
	}
	steer, steerIDs, err := s.pendingSteers(workerCtx, c)
	if err != nil {
		return err
	}
	c.SteerIDs = steerIDs
	history, err := s.SearchTraces(workerCtx, c.OrganizationID, c.TaskID, "", c.RunID, 20)
	if err != nil {
		return err
	}
	contextValue, _ := json.Marshal(map[string]any{"intent": c, "steer": steer, "other_worker_traces": history})
	_, err = s.Engine.runRoleOn(workerCtx, c.OrganizationID, c.ActorID, c.TaskID, c.IntentID, "worker", string(contextValue), obs.Summary, c.RunID)
	if err != nil {
		var blocked *llm.BlockedError
		if errors.As(err, &blocked) {
			return s.FinishWorker(workerCtx, c, "blocked", blocked.Reason, nil)
		}
		return s.FinishWorker(workerCtx, c, "blocked", err.Error(), nil)
	}
	if reason, err := s.deadlineReached(workerCtx, c.TaskID); err != nil {
		return err
	} else if reason != "" {
		return s.FinishWorker(workerCtx, c, "exhausted", reason, &obs)
	}
	return s.FinishWorker(workerCtx, c, "done", "", &obs)
}

func (s Scheduler) deadlineReached(ctx context.Context, taskID string) (string, error) {
	var reason string
	err := s.Engine.Pool.QueryRow(ctx, `SELECT CASE WHEN deadline_at<=now() THEN '任务截止时间已到' ELSE '' END FROM tasks WHERE id=$1`, taskID).Scan(&reason)
	return reason, err
}

func (s Scheduler) observe(ctx context.Context, c Claim) (Observation, error) {
	key := fmt.Sprintf("intent/%s/observation/%d", c.IntentID, c.ObservationGeneration)
	reserveTx, err := s.Engine.Pool.Begin(ctx)
	if err != nil {
		return Observation{}, err
	}
	inserted, err := reserveTx.Exec(ctx, `INSERT INTO worker_observations(intent_id,generation,idempotency_key,state) VALUES($1,$2,$3,'reserved') ON CONFLICT(intent_id,generation) DO NOTHING`, c.IntentID, c.ObservationGeneration, key)
	if err != nil {
		_ = reserveTx.Rollback(ctx)
		return Observation{}, err
	}
	if inserted.RowsAffected() == 1 {
		if _, chargeErr := reserveTx.Exec(ctx, `UPDATE task_budgets SET tool_calls=tool_calls+1,updated_at=now() WHERE task_id=$1`, c.TaskID); chargeErr != nil {
			_ = reserveTx.Rollback(ctx)
			return Observation{}, chargeErr
		}
		if err = event(ctx, reserveTx, c.OrganizationID, c.TaskID, "worker.observation_requested", map[string]any{"intent_id": c.IntentID, "generation": c.ObservationGeneration}); err != nil {
			_ = reserveTx.Rollback(ctx)
			return Observation{}, err
		}
	}
	if err = reserveTx.Commit(ctx); err != nil {
		return Observation{}, err
	}
	var state string
	var summary, toolID *string
	var confirmed bool
	err = s.Engine.Pool.QueryRow(ctx, `SELECT state,summary,tool_call_id,confirmed FROM worker_observations WHERE intent_id=$1 AND generation=$2`, c.IntentID, c.ObservationGeneration).Scan(&state, &summary, &toolID, &confirmed)
	if err != nil {
		return Observation{}, err
	}
	if state == "succeeded" && summary != nil && toolID != nil {
		return Observation{Summary: *summary, ToolCallID: *toolID, Confirmed: confirmed}, nil
	}
	obs, err := s.Observations.ObserveOnce(ctx, c.TaskID, c.IntentID, key)
	if err != nil {
		return Observation{}, err
	}
	if !obs.Confirmed || obs.ToolCallID == "" || strings.TrimSpace(obs.Summary) == "" {
		return Observation{}, task.ErrInvalid
	}
	tx, err := s.Engine.Pool.Begin(ctx)
	if err != nil {
		return Observation{}, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE worker_observations SET state='succeeded',summary=$3,tool_call_id=$4,confirmed=$5,updated_at=now() WHERE intent_id=$1 AND generation=$2 AND state='reserved'`, c.IntentID, c.ObservationGeneration, obs.Summary, obs.ToolCallID, obs.Confirmed)
	if err != nil {
		return Observation{}, err
	}
	if tag.RowsAffected() == 1 {
		if err = event(ctx, tx, c.OrganizationID, c.TaskID, "worker.observation_recorded", map[string]any{"intent_id": c.IntentID, "generation": c.ObservationGeneration, "tool_call_id": obs.ToolCallID}); err != nil {
			return Observation{}, err
		}
	} else {
		if err = tx.QueryRow(ctx, `SELECT summary,tool_call_id,confirmed FROM worker_observations WHERE intent_id=$1 AND generation=$2 AND state='succeeded'`, c.IntentID, c.ObservationGeneration).Scan(&obs.Summary, &obs.ToolCallID, &obs.Confirmed); err != nil {
			return Observation{}, err
		}
	}
	return obs, tx.Commit(ctx)
}

func (s Scheduler) pendingSteers(ctx context.Context, c Claim) ([]string, []string, error) {
	rows, err := s.Engine.Pool.Query(ctx, `SELECT id,message FROM worker_steers WHERE intent_id=$1 AND consumed_at IS NULL ORDER BY created_at,id`, c.IntentID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	messages := []string{}
	ids := []string{}
	for rows.Next() {
		var message, id string
		if err := rows.Scan(&id, &message); err != nil {
			return nil, nil, err
		}
		messages = append(messages, message)
		ids = append(ids, id)
	}
	return messages, ids, rows.Err()
}

// FinishWorker fences writes against a lost lease. The fact and terminal state
// commit together, so a restarted worker cannot duplicate a graph result.
func (s Scheduler) FinishWorker(ctx context.Context, c Claim, status, reason string, obs *Observation) error {
	if status != "done" && status != "blocked" && status != "exhausted" && status != "paused" && status != "stopped" {
		return task.ErrInvalid
	}
	tx, err := s.Engine.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var taskStatus string
	err = tx.QueryRow(ctx, `SELECT t.status FROM tasks t JOIN worker_leases l ON l.task_id=t.id WHERE l.intent_id=$1 AND l.run_id=$2 AND l.fence=$3 AND l.expires_at>now() FOR UPDATE OF t,l`, c.IntentID, c.RunID, c.Fence).Scan(&taskStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return task.ErrConflict
	}
	if err != nil {
		return err
	}
	if taskStatus == "paused" {
		status, reason, obs = "paused", "任务已暂停", nil
	} else if taskStatus == "cancelled" {
		status, reason, obs = "stopped", "任务已取消", nil
	}
	if status == "done" && obs == nil {
		return task.ErrInvalid
	}
	if obs != nil {
		factID := identity.NewID()
		_, err = tx.Exec(ctx, `INSERT INTO exploration_nodes(id,task_id,kind,state,summary,source_intent_id,run_id,tool_call_id) VALUES($1,$2,'fact','confirmed',$3,$4,$5,$6)`, factID, c.TaskID, obs.Summary, c.IntentID, c.RunID, obs.ToolCallID)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO exploration_edges(task_id,from_node_id,to_node_id,kind) VALUES($1,$2,$3,'yields')`, c.TaskID, c.IntentID, factID); err != nil {
			return err
		}
		if err = event(ctx, tx, c.OrganizationID, c.TaskID, "fact.created", map[string]any{"fact_id": factID, "intent_id": c.IntentID, "run_id": c.RunID}); err != nil {
			return err
		}
	}
	if status == "exhausted" {
		settlement := map[string]any{"status": "exhausted", "reason": reason, "partial_observation": obs != nil}
		if obs != nil {
			settlement["summary"] = obs.Summary
			settlement["tool_call_id"] = obs.ToolCallID
		}
		encoded, _ := json.Marshal(settlement)
		if _, err = tx.Exec(ctx, `INSERT INTO agent_messages(id,run_id,ordinal,role,trust_level,content) SELECT $1,$2,COALESCE(max(ordinal)+1,0),'assistant','trusted',$3 FROM agent_messages WHERE run_id=$2`, identity.NewID(), c.RunID, string(encoded)); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE task_intents SET status=$2,blocking_reason=NULLIF($3,''),updated_at=now() WHERE id=$1`, c.IntentID, status, reason)
	if err != nil {
		return err
	}
	if status == "done" {
		if _, err = tx.Exec(ctx, `UPDATE task_todos SET status='done',updated_at=now() WHERE intent_id=$1 AND status='open'`, c.IntentID); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE exploration_nodes SET state=$2,updated_at=now() WHERE id=$1`, c.IntentID, status)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE agent_runs SET status=CASE WHEN $2='done' THEN 'succeeded' ELSE $2 END,error_class=NULLIF($3,''),completed_at=now() WHERE id=$1`, c.RunID, status, reason)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE worker_steers SET consumed_run_id=$2,consumed_at=now() WHERE id=ANY($1::uuid[]) AND consumed_at IS NULL`, c.SteerIDs, c.RunID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM worker_leases WHERE intent_id=$1`, c.IntentID)
	if err != nil {
		return err
	}
	if err = event(ctx, tx, c.OrganizationID, c.TaskID, "intent."+status, map[string]any{"intent_id": c.IntentID, "attempt": c.Attempt, "reason": reason}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Reconcile expires abandoned leases and preserves old runs for audit.
func (s Scheduler) Reconcile(ctx context.Context) (int, error) {
	tx, err := s.Engine.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT l.intent_id,l.run_id,l.task_id,i.attempt,i.max_attempts,t.organization_id,t.status FROM worker_leases l JOIN task_intents i ON i.id=l.intent_id JOIN tasks t ON t.id=l.task_id WHERE l.expires_at<now() FOR UPDATE OF l SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	type expired struct {
		intentID, runID, taskID, orgID, taskStatus string
		attempt, max                               int
	}
	items := []expired{}
	for rows.Next() {
		var x expired
		if err := rows.Scan(&x.intentID, &x.runID, &x.taskID, &x.attempt, &x.max, &x.orgID, &x.taskStatus); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, x := range items {
		status, reason := "open", "租约到期，等待重新领取"
		if x.attempt >= x.max {
			status, reason = "blocked", "已达到最大重试次数；请检查运行记录后人工重试"
		} else if x.taskStatus == "paused" {
			status, reason = "paused", "任务已暂停"
		} else if x.taskStatus == "cancelled" {
			status, reason = "stopped", "任务已取消"
		}
		if _, err = tx.Exec(ctx, `UPDATE task_intents SET status=$2,blocking_reason=$3,updated_at=now() WHERE id=$1`, x.intentID, status, reason); err != nil {
			return 0, err
		}
		if _, err = tx.Exec(ctx, `UPDATE exploration_nodes SET state=$2,updated_at=now() WHERE id=$1`, x.intentID, status); err != nil {
			return 0, err
		}
		if _, err = tx.Exec(ctx, `UPDATE agent_runs SET status='interrupted',error_class='lease_expired',completed_at=now() WHERE id=$1 AND status='running'`, x.runID); err != nil {
			return 0, err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM worker_leases WHERE intent_id=$1`, x.intentID); err != nil {
			return 0, err
		}
		if err = event(ctx, tx, x.orgID, x.taskID, "intent.reconciled", map[string]any{"intent_id": x.intentID, "status": status, "reason": reason}); err != nil {
			return 0, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE planner_wakes SET pending=true,owner_id=NULL,expires_at=NULL,due_at=now(),updated_at=now() WHERE expires_at<now()`); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO planner_wakes(task_id,due_at,reason) SELECT t.id,now(),'reconciliation' FROM tasks t WHERE t.status IN ('queued','running') AND NOT EXISTS(SELECT 1 FROM planner_wakes w WHERE w.task_id=t.id) ON CONFLICT DO NOTHING`); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE planner_wakes w SET pending=true,due_at=now(),reason='heartbeat',updated_at=now() FROM tasks t WHERE w.task_id=t.id AND t.status='running' AND NOT w.pending AND w.expires_at IS NULL AND w.updated_at<now()-interval '5 minutes' AND EXISTS(SELECT 1 FROM task_goals g WHERE g.task_id=t.id AND g.state='open')`); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_runs r SET status='interrupted',error_class='orphaned_after_restart',completed_at=now() WHERE r.status='running' AND r.started_at<now()-interval '30 seconds' AND NOT EXISTS(SELECT 1 FROM worker_leases l WHERE l.run_id=r.id AND l.expires_at>now()) AND NOT EXISTS(SELECT 1 FROM planner_wakes w WHERE w.task_id=r.task_id AND w.expires_at>now())`); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE tool_calls c SET status='failed',error_class='process_interrupted',summary='进程中断，执行结果未确认；请使用新的调用重试',completed_at=now() WHERE c.status='running' AND c.started_at<now()-interval '30 seconds' AND NOT EXISTS(SELECT 1 FROM worker_leases l WHERE l.run_id=c.run_id AND l.expires_at>now())`); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE llm_attempts a SET status='failed',error_class='process_interrupted',completed_at=now() FROM agent_runs r WHERE a.run_id=r.id AND a.status='running' AND r.status IN ('interrupted','stopped','paused','blocked','failed')`); err != nil {
		return 0, err
	}
	deadlineRows, err := tx.Query(ctx, `UPDATE task_intents i SET status='exhausted',blocking_reason='任务截止时间已到',updated_at=now() FROM tasks t WHERE i.task_id=t.id AND i.status='open' AND t.status='running' AND t.deadline_at<=now() RETURNING i.id,t.id,t.organization_id`)
	if err != nil {
		return 0, err
	}
	type deadlineIntent struct{ intentID, taskID, orgID string }
	deadlineItems := []deadlineIntent{}
	for deadlineRows.Next() {
		var x deadlineIntent
		if err = deadlineRows.Scan(&x.intentID, &x.taskID, &x.orgID); err != nil {
			deadlineRows.Close()
			return 0, err
		}
		deadlineItems = append(deadlineItems, x)
	}
	err = deadlineRows.Err()
	deadlineRows.Close()
	if err != nil {
		return 0, err
	}
	for _, x := range deadlineItems {
		if _, err = tx.Exec(ctx, `UPDATE exploration_nodes SET state='exhausted',updated_at=now() WHERE id=$1`, x.intentID); err != nil {
			return 0, err
		}
		if err = event(ctx, tx, x.orgID, x.taskID, "intent.exhausted", map[string]any{"intent_id": x.intentID, "reason": "任务截止时间已到"}); err != nil {
			return 0, err
		}
	}
	return len(items), tx.Commit(ctx)
}

func (s Scheduler) RunPlanner(ctx context.Context, c Claim) error {
	plannerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(leaseDuration / 3)
		defer ticker.Stop()
		for {
			select {
			case <-plannerCtx.Done():
				return
			case <-ticker.C:
				tag, err := s.Engine.Pool.Exec(plannerCtx, `UPDATE planner_wakes SET expires_at=now()+interval '30 seconds' WHERE task_id=$1 AND fence=$2 AND expires_at>now()`, c.TaskID, c.Fence)
				if err != nil || tag.RowsAffected() != 1 {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-finished }()
	detail, err := (task.Store{Pool: s.Engine.Pool}).Get(plannerCtx, c.OrganizationID, c.TaskID)
	if err != nil {
		return err
	}
	if detail.Task.Status != "running" {
		return nil
	}
	var activeGoals int
	if err := s.Engine.Pool.QueryRow(plannerCtx, `SELECT count(*) FROM task_goals WHERE task_id=$1 AND state='open'`, c.TaskID).Scan(&activeGoals); err != nil {
		return err
	}
	if activeGoals == 0 {
		_, err := s.Engine.Pool.Exec(plannerCtx, `UPDATE tasks SET status='completed',version=version+1,updated_at=now() WHERE id=$1 AND status='running' AND NOT EXISTS(SELECT 1 FROM task_intents WHERE task_id=$1 AND status IN ('open','leased','running','paused'))`, c.TaskID)
		return err
	}
	intents, err := s.Engine.ListIntents(plannerCtx, c.OrganizationID, c.TaskID)
	if err != nil {
		return err
	}
	var directions string
	if len(intents) == 0 {
		input, _ := json.Marshal(map[string]any{"task": detail.Task, "goals": detail.Goals, "scope": detail.Scope, "constraints": detail.Constraints})
		directions, err = s.Engine.runRole(plannerCtx, c.OrganizationID, c.ActorID, c.TaskID, "", "decomposer", string(input), "")
		if err != nil {
			return err
		}
		if err = validateDirections(directions, detail.Goals); err != nil {
			return err
		}
	}
	graph, err := (task.Store{Pool: s.Engine.Pool}).Graph(plannerCtx, c.OrganizationID, c.TaskID)
	if err != nil {
		return err
	}
	todos, err := s.ListTodos(plannerCtx, c.OrganizationID, c.TaskID)
	if err != nil {
		return err
	}
	triggerEvents, err := s.plannerEvents(plannerCtx, c)
	if err != nil {
		return err
	}
	var activeWorkers int
	if err = s.Engine.Pool.QueryRow(plannerCtx, `SELECT count(*) FROM worker_leases WHERE task_id=$1 AND expires_at>now()`, c.TaskID).Scan(&activeWorkers); err != nil {
		return err
	}
	input, _ := json.Marshal(map[string]any{"task": detail.Task, "goals": detail.Goals, "scope": detail.Scope, "constraints": detail.Constraints, "graph": graph, "intents": intents, "todos": todos, "decomposer_output": directions, "trigger_events": triggerEvents, "available_worker_slots": detail.Task.WorkerLimit - activeWorkers})
	plan, err := s.Engine.runRole(plannerCtx, c.OrganizationID, c.ActorID, c.TaskID, "", "planner", string(input), "")
	if err != nil {
		return err
	}
	var stillRunning bool
	if err := s.Engine.Pool.QueryRow(plannerCtx, `SELECT EXISTS(SELECT 1 FROM tasks t JOIN planner_wakes w ON w.task_id=t.id WHERE t.id=$1 AND t.status='running' AND w.fence=$2 AND w.expires_at>now())`, c.TaskID, c.Fence).Scan(&stillRunning); err != nil {
		return err
	}
	if !stillRunning {
		return task.ErrConflict
	}
	_, err = s.Engine.insertIntents(plannerCtx, c.OrganizationID, c.TaskID, plan, detail.Task.WorkerLimit)
	return err
}

func (s Scheduler) plannerEvents(ctx context.Context, c Claim) ([]map[string]any, error) {
	rows, err := s.Engine.Pool.Query(ctx, `SELECT event_type,payload,occurred_at FROM domain_events WHERE task_id=$1 AND ($2::timestamptz IS NULL OR occurred_at>$2) AND occurred_at<=$3 ORDER BY occurred_at,id`, c.TaskID, c.PlannerSince, c.PlannerClaimedAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var kind string
		var payload json.RawMessage
		var at time.Time
		if err = rows.Scan(&kind, &payload, &at); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"type": kind, "payload": payload, "at": at})
	}
	return items, rows.Err()
}

func (s Scheduler) FinishPlanner(ctx context.Context, c Claim, runErr error) error {
	tx, err := s.Engine.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var fence int64
	err = tx.QueryRow(ctx, `SELECT fence FROM planner_wakes WHERE task_id=$1 AND fence=$2 FOR UPDATE`, c.TaskID, c.Fence).Scan(&fence)
	if errors.Is(err, pgx.ErrNoRows) {
		return task.ErrConflict
	}
	if err != nil {
		return err
	}
	if runErr != nil {
		_, err = tx.Exec(ctx, `UPDATE planner_wakes SET pending=true,due_at=now()+interval '10 seconds',reason=$2,owner_id=NULL,expires_at=NULL,last_planned_at=$3,updated_at=now() WHERE task_id=$1`, c.TaskID, runErr.Error(), c.PlannerSince)
		if err == nil {
			err = event(ctx, tx, c.OrganizationID, c.TaskID, "agent.planning_blocked", map[string]any{"reason": runErr.Error(), "retry_in_seconds": 10})
		}
	} else {
		_, err = tx.Exec(ctx, `UPDATE planner_wakes SET owner_id=NULL,expires_at=NULL,updated_at=now() WHERE task_id=$1`, c.TaskID)
		if err == nil {
			err = event(ctx, tx, c.OrganizationID, c.TaskID, "planner.round_completed", map[string]any{"task_id": c.TaskID, "fence": c.Fence})
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Run polls durable work; multiple processes may run it against one database.
func (s Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	sem := make(chan struct{}, 8)
	lastMaintenance := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if time.Since(lastMaintenance) >= time.Second {
			_, _ = s.Reconcile(ctx)
			_, _ = s.PumpEvents(ctx, 100)
			lastMaintenance = time.Now()
		}
		if len(sem) < cap(sem) {
			if c, ok, err := s.ClaimPlanner(ctx); err == nil && ok {
				sem <- struct{}{}
				go func() {
					defer func() { <-sem }()
					err := s.RunPlanner(ctx, c)
					if finishErr := s.FinishPlanner(context.Background(), c, err); finishErr != nil && s.Logger != nil {
						s.Logger.Error("finish planner", "error", finishErr)
					}
				}()
			} else if err != nil && s.Logger != nil {
				s.Logger.Error("claim planner", "error", err)
			}
		}
		for len(sem) < cap(sem) {
			c, ok, err := s.ClaimWorker(ctx)
			if err != nil {
				if s.Logger != nil {
					s.Logger.Error("claim worker", "error", err)
				}
				break
			}
			if !ok {
				break
			}
			sem <- struct{}{}
			go func() {
				defer func() { <-sem }()
				if err := s.RunWorker(ctx, c); err != nil && s.Logger != nil {
					s.Logger.Error("run worker", "error", err)
				}
			}()
		}
	}
}
