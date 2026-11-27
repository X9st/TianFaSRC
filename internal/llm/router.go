package llm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/jackc/pgx/v5"
)

type BlockedError struct {
	Until  *time.Time
	Reason string
}

var ErrWorkerBudgetExhausted = errors.New("Worker 模型预算已用尽")

func (e *BlockedError) Error() string { return e.Reason }

type Router struct {
	Store    Store
	Provider HTTPProvider
}

// Complete persists every provider attempt before returning text to the Agent.
func (r Router) Complete(ctx context.Context, orgID, taskID, runID, role string, messages []Message) (Completion, error) {
	profiles, err := r.Store.ResolvedChain(ctx, orgID, taskID, role)
	if err != nil {
		return Completion{}, err
	}
	if len(profiles) == 0 {
		return Completion{}, &BlockedError{Reason: "没有可用的模型 Profile；请配置主备链"}
	}
	if role == "worker" {
		// Reserve once per logical model round, including provider failover.
		tag, err := r.Store.Pool.Exec(ctx, `UPDATE worker_run_budgets SET model_rounds_used=model_rounds_used+1,updated_at=now() WHERE run_id=$1 AND organization_id=$2 AND task_id=$3 AND model_rounds_used<max_model_rounds AND input_tokens_used+output_tokens_used<max_tokens AND started_at+max_seconds*interval '1 second'>now()`, runID, orgID, taskID)
		if err != nil {
			return Completion{}, err
		}
		if tag.RowsAffected() != 1 {
			return Completion{}, ErrWorkerBudgetExhausted
		}
	}
	var earliest *time.Time
	var previous string
	var lastClass string
	var partial string
	for i, p := range profiles {
		if role == "worker" {
			var remaining int
			if err := r.Store.Pool.QueryRow(ctx, `SELECT max_tokens-input_tokens_used-output_tokens_used FROM worker_run_budgets WHERE run_id=$1`, runID).Scan(&remaining); err != nil {
				return Completion{}, err
			}
			if remaining <= 0 {
				return Completion{}, ErrWorkerBudgetExhausted
			}
			if p.MaxOutputTokens > remaining {
				p.MaxOutputTokens = remaining
			}
		}
		estimatedTokens := 0
		for _, message := range messages {
			estimatedTokens += len(message.Content)/4 + 8
		}
		if role == "worker" {
			var remaining int
			if err := r.Store.Pool.QueryRow(ctx, `SELECT max_tokens-input_tokens_used-output_tokens_used FROM worker_run_budgets WHERE run_id=$1`, runID).Scan(&remaining); err != nil {
				return Completion{}, err
			}
			if estimatedTokens >= remaining {
				return Completion{}, ErrWorkerBudgetExhausted
			}
			if p.MaxOutputTokens > remaining-estimatedTokens {
				p.MaxOutputTokens = remaining - estimatedTokens
			}
		}
		if estimatedTokens+p.MaxOutputTokens+512 >= p.ContextWindow {
			continue
		}
		allowed, until, err := r.reserve(ctx, p)
		if err != nil {
			return Completion{}, err
		}
		if !allowed {
			if until != nil && (earliest == nil || until.Before(*earliest)) {
				earliest = until
			}
			continue
		}
		if partial != "" {
			messages = append(append([]Message{}, messages...), Message{Role: "assistant", Content: partial}, Message{Role: "user", Content: "上一模型的流中断。上方 assistant 内容已交付并持久化；请只接着未完成部分继续，不重复前文。"})
		}
		if previous != "" {
			if err := r.failoverEvent(ctx, orgID, taskID, runID, previous, p.ID, lastClass, i); err != nil {
				return Completion{}, err
			}
		}
		credential, err := r.Store.Credential(ctx, orgID, p.ID)
		if err != nil {
			return Completion{}, err
		}
		attemptID := identity.NewID()
		var ordinal int
		if err := r.Store.Pool.QueryRow(ctx, `UPDATE agent_runs SET llm_profile_attempt=llm_profile_attempt+1 WHERE id=$1 RETURNING llm_profile_attempt`, runID).Scan(&ordinal); err != nil {
			return Completion{}, err
		}
		if _, err := r.Store.Pool.Exec(ctx, `INSERT INTO llm_attempts(id,run_id,profile_id,ordinal,status) VALUES($1,$2,$3,$4,'running')`, attemptID, runID, p.ID, ordinal); err != nil {
			return Completion{}, err
		}
		started := time.Now()
		result, callErr := r.Provider.Complete(ctx, p, credential, messages)
		status, class := "succeeded", ""
		if callErr != nil {
			status = "failed"
			var pe *ProviderError
			if errors.As(callErr, &pe) {
				class = pe.Class
				if pe.Partial {
					status = "partial"
				}
			} else {
				class = "internal"
			}
		}
		settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		tx, err := r.Store.Pool.Begin(settleCtx)
		if err != nil {
			cancel()
			return Completion{}, err
		}
		if _, err := tx.Exec(settleCtx, `UPDATE llm_attempts SET status=$2,error_class=NULLIF($3,''),delivered_text=$4,input_tokens=$5,output_tokens=$6,cache_read_tokens=$7,cache_write_tokens=$8,reasoning_tokens=$9,estimated=$10,latency_ms=$11,first_token_ms=$12,completed_at=now() WHERE id=$1`, attemptID, status, class, result.Text, result.Usage.InputTokens, result.Usage.OutputTokens, result.Usage.CacheReadTokens, result.Usage.CacheWriteTokens, result.Usage.ReasoningTokens, result.Usage.Estimated, time.Since(started).Milliseconds(), result.FirstToken.Milliseconds()); err != nil {
			_ = tx.Rollback(settleCtx)
			cancel()
			return Completion{}, err
		}
		if _, err := tx.Exec(settleCtx, `UPDATE task_budgets SET input_tokens=input_tokens+$2,output_tokens=output_tokens+$3,updated_at=now() WHERE task_id=$1`, taskID, result.Usage.InputTokens, result.Usage.OutputTokens); err != nil {
			_ = tx.Rollback(settleCtx)
			cancel()
			return Completion{}, err
		}
		if role == "worker" {
			if _, err := tx.Exec(settleCtx, `UPDATE worker_run_budgets SET input_tokens_used=input_tokens_used+$2,output_tokens_used=output_tokens_used+$3,updated_at=now() WHERE run_id=$1`, runID, result.Usage.InputTokens, result.Usage.OutputTokens); err != nil {
				_ = tx.Rollback(settleCtx)
				cancel()
				return Completion{}, err
			}
		}
		if _, err := tx.Exec(settleCtx, `UPDATE agent_runs SET profile_id=$2 WHERE id=$1`, runID, p.ID); err != nil {
			_ = tx.Rollback(settleCtx)
			cancel()
			return Completion{}, err
		}
		if err := tx.Commit(settleCtx); err != nil {
			cancel()
			return Completion{}, err
		}
		if err := r.recordHealth(settleCtx, p.ID, callErr); err != nil {
			cancel()
			return Completion{}, err
		}
		cancel()
		if callErr == nil {
			result.Text = partial + result.Text
			return result, nil
		}
		if class == "cancelled" || class == "invalid_request" || result.ToolState {
			return result, callErr
		}
		if result.Text != "" {
			partial += result.Text
		}
		previous = p.ID
		lastClass = class
	}
	return Completion{Text: partial}, &BlockedError{Until: earliest, Reason: fmt.Sprintf("模型主备链暂时不可用：%s", humanError(lastClass))}
}

func humanError(class string) string {
	switch class {
	case "transport":
		return "无法连接模型服务"
	case "quota":
		return "模型配额已用尽"
	case "rate_limit":
		return "模型服务限流"
	case "auth":
		return "模型凭据无效"
	case "model_not_found":
		return "模型不存在"
	case "stream_interrupted":
		return "响应流中断"
	case "":
		return "无可用 Profile 或上下文窗口不足"
	default:
		return "模型服务异常"
	}
}

func (r Router) reserve(ctx context.Context, p Profile) (bool, *time.Time, error) {
	tx, err := r.Store.Pool.Begin(ctx)
	if err != nil {
		return false, nil, err
	}
	defer tx.Rollback(ctx)
	var enabled bool
	var state string
	var cooldown, probe *time.Time
	var secondAt, minuteAt time.Time
	var secondUsed, minuteUsed, perSecond, perMinute int
	err = tx.QueryRow(ctx, `SELECT enabled,circuit_state,cooldown_until,probe_lease_until,second_window_at,second_used,minute_window_at,minute_used,rate_per_second,rate_per_minute FROM llm_profiles WHERE id=$1 FOR UPDATE`, p.ID).Scan(&enabled, &state, &cooldown, &probe, &secondAt, &secondUsed, &minuteAt, &minuteUsed, &perSecond, &perMinute)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil, ErrNotFound
	}
	if err != nil {
		return false, nil, err
	}
	now := time.Now()
	if !enabled {
		return false, nil, nil
	}
	if cooldown != nil && cooldown.After(now) {
		return false, cooldown, nil
	}
	if (state == "open" || state == "half_open") && probe != nil && probe.After(now) {
		return false, probe, nil
	}
	if now.Sub(secondAt) >= time.Second {
		secondAt = now
		secondUsed = 0
	}
	if now.Sub(minuteAt) >= time.Minute {
		minuteAt = now
		minuteUsed = 0
	}
	if secondUsed >= perSecond {
		next := secondAt.Add(time.Second)
		return false, &next, nil
	}
	if minuteUsed >= perMinute {
		next := minuteAt.Add(time.Minute)
		return false, &next, nil
	}
	if state == "open" || state == "half_open" {
		state = "half_open"
		until := now.Add(2 * time.Minute)
		probe = &until
	}
	_, err = tx.Exec(ctx, `UPDATE llm_profiles SET circuit_state=$2,health_state=CASE WHEN $2='half_open' THEN 'half_open' ELSE health_state END,probe_lease_until=$3,second_window_at=$4,second_used=$5,minute_window_at=$6,minute_used=$7 WHERE id=$1`, p.ID, state, probe, secondAt, secondUsed+1, minuteAt, minuteUsed+1)
	if err != nil {
		return false, nil, err
	}
	return true, nil, tx.Commit(ctx)
}

func (r Router) recordHealth(ctx context.Context, id string, callErr error) error {
	if callErr == nil {
		_, err := r.Store.Pool.Exec(ctx, `UPDATE llm_profiles SET health_state='healthy',circuit_state='closed',soft_failures=0,cooldown_until=NULL,probe_lease_until=NULL,last_success_at=now(),updated_at=now() WHERE id=$1`, id)
		return err
	}
	var pe *ProviderError
	if !errors.As(callErr, &pe) {
		return nil
	}
	if pe.Class == "cancelled" || pe.Class == "invalid_request" {
		_, err := r.Store.Pool.Exec(ctx, `UPDATE llm_profiles SET circuit_state='closed',probe_lease_until=NULL WHERE id=$1`, id)
		return err
	}
	hard := pe.Class == "auth" || pe.Class == "quota" || pe.Class == "model_not_found"
	cooldown := 30 * time.Second
	if pe.RetryAfter > cooldown {
		cooldown = pe.RetryAfter
	}
	if pe.Class == "quota" {
		cooldown = 24 * time.Hour
	}
	if pe.Class == "auth" || pe.Class == "model_not_found" {
		cooldown = time.Hour
	}
	tx, err := r.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var failures int
	var state string
	var last *time.Time
	if err := tx.QueryRow(ctx, `SELECT soft_failures,circuit_state,last_failure_at FROM llm_profiles WHERE id=$1 FOR UPDATE`, id).Scan(&failures, &state, &last); err != nil {
		return err
	}
	if last == nil || time.Since(*last) > time.Minute {
		failures = 0
	}
	failures++
	health := "degraded"
	if hard || failures >= 3 || state == "half_open" {
		state = "open"
		health = "open_circuit"
	} else {
		state = "closed"
	}
	_, err = tx.Exec(ctx, `UPDATE llm_profiles SET circuit_state=$2,health_state=$3,soft_failures=$4,cooldown_until=now()+($5::bigint * interval '1 millisecond'),probe_lease_until=NULL,last_failure_at=now(),updated_at=now() WHERE id=$1`, id, state, health, failures, cooldown.Milliseconds())
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r Router) failoverEvent(ctx context.Context, orgID, taskID, runID, from, to, class string, retries int) error {
	_, err := r.Store.Pool.Exec(ctx, `INSERT INTO domain_events(id,organization_id,task_id,event_type,payload) VALUES($1,$2,$3,'llm.failover',$4)`, identity.NewID(), orgID, taskID, map[string]any{"schema_version": 1, "data": map[string]any{"run_id": runID, "from_profile_id": from, "to_profile_id": to, "error_class": class, "retry_count": retries}})
	return err
}
