package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/X9st/TianFaSRC/internal/blob"
	"github.com/X9st/TianFaSRC/internal/finding"
	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/mcpclient"
	"github.com/X9st/TianFaSRC/internal/policy"
	"github.com/X9st/TianFaSRC/internal/skill"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/X9st/TianFaSRC/internal/traffic"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrApprovalRequired = errors.New("工具调用等待审批")
var ErrInProgress = errors.New("工具调用正在执行")
var ErrToolBlocked = errors.New("工具调用被策略阻断")
var ErrWorkerBudgetExhausted = errors.New("Worker 工具预算已用尽")

type Call struct {
	OrganizationID string
	TaskID         string
	IntentID       string
	RunID          string
	ActorID        string
	Name           string
	Input          json.RawMessage
	IdempotencyKey string
}

type Outcome struct {
	ID           string          `json:"id"`
	Status       string          `json:"status"`
	Summary      string          `json:"summary"`
	Output       json.RawMessage `json:"output"`
	ScopeVersion int64           `json:"scope_version"`
}

type Dispatcher struct {
	Pool         *pgxpool.Pool
	DataDir      string
	Enforcer     policy.Enforcer
	CommandImage string
}

func Names() []string {
	return []string{"http.read", "workspace.read", "workspace.write", "workspace.list", "skill.load", "finding.report", "command.run"}
}

func (d Dispatcher) AvailableNames() []string {
	if d.CommandImage != "" {
		return Names()
	}
	return Names()[:6]
}

func (d Dispatcher) AvailableForRun(ctx context.Context, orgID, runID string) ([]string, error) {
	bindings, err := d.Bindings(ctx, orgID)
	if err != nil {
		return nil, err
	}
	names := []string{}
	bound := func(name string) bool { enabled, configured := bindings[name]; return !configured || enabled }
	for _, name := range d.AvailableNames() {
		if bound(name) {
			names = append(names, name)
		}
	}
	custom, err := d.ListCustom(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for _, item := range custom {
		if item.Enabled && bound("custom__"+item.Name) {
			names = append(names, "custom__"+item.Name)
		}
	}
	rows, err := d.Pool.Query(ctx, `SELECT ms.name,mt.name FROM mcp_tools mt JOIN mcp_servers ms ON ms.id=mt.server_id WHERE ms.organization_id=$1 AND ms.enabled AND mt.enabled AND EXISTS(SELECT 1 FROM run_loaded_skills rls JOIN skills sk ON sk.id=rls.skill_id WHERE rls.run_id=$2 AND sk.enabled AND ms.name=ANY(sk.mcps)) ORDER BY ms.name,mt.name`, orgID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var server, name string
		if err := rows.Scan(&server, &name); err != nil {
			return nil, err
		}
		fullName := "mcp__" + server + "__" + name
		if bound(fullName) {
			names = append(names, fullName)
		}
	}
	return names, rows.Err()
}

func known(name string) bool {
	for _, item := range Names() {
		if item == name {
			return true
		}
	}
	return false
}

func validateInput(name string, raw json.RawMessage) error {
	switch name {
	case "http.read":
		var value HTTPInput
		if err := decodeStrict(raw, &value); err != nil || value.URL == "" {
			return task.ErrInvalid
		}
	case "workspace.read", "workspace.write":
		var value fileInput
		if err := decodeStrict(raw, &value); err != nil || !safeName(value.Name) {
			return task.ErrInvalid
		}
	case "workspace.list":
		var value struct{}
		if err := decodeStrict(raw, &value); err != nil {
			return task.ErrInvalid
		}
	case "command.run":
		var value commandInput
		if err := decodeStrict(raw, &value); err != nil || len(value.Args) == 0 {
			return task.ErrInvalid
		}
	case "skill.load":
		var value struct {
			Name string `json:"name"`
		}
		if err := decodeStrict(raw, &value); err != nil || value.Name == "" {
			return task.ErrInvalid
		}
	case "finding.report":
		var value finding.Input
		if err := decodeStrict(raw, &value); err != nil {
			return task.ErrInvalid
		}
		if value.TaskID != "" || value.SourceIntentID != "" || value.SourceRunID != "" || len(value.AssetIDs) == 0 {
			return task.ErrInvalid
		}
	default:
		return task.ErrInvalid
	}
	return nil
}

func splitMCPName(name string) (string, string, bool) {
	if !strings.HasPrefix(name, "mcp__") {
		return "", "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(name, "mcp__"), "__", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (d Dispatcher) validateCall(ctx context.Context, call Call) error {
	if known(call.Name) {
		return validateInput(call.Name, call.Input)
	}
	if strings.HasPrefix(call.Name, "custom__") {
		item, err := d.custom(ctx, call.OrganizationID, strings.TrimPrefix(call.Name, "custom__"))
		if err != nil || !item.Enabled {
			return task.ErrInvalid
		}
		return validateSchema(item.InputSchema, call.Input)
	}
	server, name, ok := splitMCPName(call.Name)
	if !ok {
		return task.ErrInvalid
	}
	var schema json.RawMessage
	var enabled bool
	if err := d.Pool.QueryRow(ctx, `SELECT mt.input_schema,ms.enabled AND mt.enabled AND EXISTS(SELECT 1 FROM run_loaded_skills rls JOIN skills sk ON sk.id=rls.skill_id WHERE rls.run_id=$4 AND sk.enabled AND ms.name=ANY(sk.mcps)) FROM mcp_tools mt JOIN mcp_servers ms ON ms.id=mt.server_id WHERE ms.organization_id=$1 AND ms.name=$2 AND mt.name=$3`, call.OrganizationID, server, name, call.RunID).Scan(&schema, &enabled); err != nil || !enabled {
		return task.ErrInvalid
	}
	return validateSchema(schema, call.Input)
}

func sameJSON(a, b json.RawMessage) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	canonicalLeft, _ := json.Marshal(left)
	canonicalRight, _ := json.Marshal(right)
	return string(canonicalLeft) == string(canonicalRight)
}

func (d Dispatcher) Execute(ctx context.Context, call Call) (Outcome, error) {
	if (call.Name == "command.run" && d.CommandImage == "") || len(call.Input) == 0 || len(call.Input) > 1<<20 || len(call.IdempotencyKey) < 8 || len(call.IdempotencyKey) > 240 {
		return Outcome{}, task.ErrInvalid
	}
	var prior Outcome
	var priorName, priorTask, priorIntent, priorOrg string
	var priorInput json.RawMessage
	err := d.Pool.QueryRow(ctx, `SELECT id,status,coalesce(summary,''),output,scope_version,name,input,task_id,intent_id,organization_id FROM tool_calls WHERE idempotency_key=$1`, call.IdempotencyKey).Scan(&prior.ID, &prior.Status, &prior.Summary, &prior.Output, &prior.ScopeVersion, &priorName, &priorInput, &priorTask, &priorIntent, &priorOrg)
	if err == nil {
		if priorName != call.Name || !sameJSON(priorInput, call.Input) || priorTask != call.TaskID || priorIntent != call.IntentID || priorOrg != call.OrganizationID {
			return Outcome{}, task.ErrConflict
		}
		switch prior.Status {
		case "succeeded":
			return prior, nil
		case "running", "queued":
			return prior, ErrInProgress
		case "blocked", "failed", "cancelled":
			return prior, ErrToolBlocked
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Outcome{}, err
	}
	var valid bool
	if err := d.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs r JOIN tasks t ON t.id=r.task_id JOIN task_intents i ON i.id=r.intent_id WHERE r.id=$1 AND r.role='worker' AND r.status='running' AND r.task_id=$2 AND r.intent_id=$3 AND r.organization_id=$4 AND t.organization_id=$4 AND t.status='running' AND i.status='running')`, call.RunID, call.TaskID, call.IntentID, call.OrganizationID).Scan(&valid); err != nil {
		return Outcome{}, err
	}
	if !valid {
		return Outcome{}, task.ErrConflict
	}
	detail, err := (task.Store{Pool: d.Pool}).Get(ctx, call.OrganizationID, call.TaskID)
	if err != nil {
		return Outcome{}, err
	}
	id := identity.NewID()
	reserveTx, err := d.Pool.Begin(ctx)
	if err != nil {
		return Outcome{}, err
	}
	defer reserveTx.Rollback(ctx)
	var used, maximum int
	var withinTime bool
	err = reserveTx.QueryRow(ctx, `SELECT tool_calls_used,max_tool_calls,started_at+max_seconds*interval '1 second'>now() FROM worker_run_budgets WHERE run_id=$1 AND organization_id=$2 AND task_id=$3 AND intent_id=$4 FOR UPDATE`, call.RunID, call.OrganizationID, call.TaskID, call.IntentID).Scan(&used, &maximum, &withinTime)
	if errors.Is(err, pgx.ErrNoRows) {
		return Outcome{}, ErrWorkerBudgetExhausted
	}
	if err != nil {
		return Outcome{}, err
	}
	if used >= maximum || !withinTime {
		return Outcome{}, ErrWorkerBudgetExhausted
	}
	inserted, err := reserveTx.Exec(ctx, `INSERT INTO tool_calls(id,organization_id,task_id,intent_id,run_id,agent_key,name,input,idempotency_key,scope_version,status) VALUES($1,$2,$3,$4,$5,'worker',$6,$7,$8,$9,'queued') ON CONFLICT(idempotency_key) DO NOTHING`, id, call.OrganizationID, call.TaskID, call.IntentID, call.RunID, call.Name, call.Input, call.IdempotencyKey, detail.Task.ScopeVersion)
	if err != nil {
		return Outcome{}, err
	}
	if inserted.RowsAffected() == 1 {
		if _, err = reserveTx.Exec(ctx, `UPDATE worker_run_budgets SET tool_calls_used=tool_calls_used+1,updated_at=now() WHERE run_id=$1`, call.RunID); err != nil {
			return Outcome{}, err
		}
	}
	if err = reserveTx.Commit(ctx); err != nil {
		return Outcome{}, err
	}
	var status, existingName, existingTask, existingIntent, existingOrg string
	var existingInput json.RawMessage
	var summary *string
	var output json.RawMessage
	var scopeVersion int64
	if err = d.Pool.QueryRow(ctx, `SELECT id,status,name,input,summary,output,scope_version,task_id,intent_id,organization_id FROM tool_calls WHERE idempotency_key=$1`, call.IdempotencyKey).Scan(&id, &status, &existingName, &existingInput, &summary, &output, &scopeVersion, &existingTask, &existingIntent, &existingOrg); err != nil {
		return Outcome{}, err
	}
	if inserted.RowsAffected() == 0 {
		if existingName != call.Name || !sameJSON(existingInput, call.Input) || existingTask != call.TaskID || existingIntent != call.IntentID || existingOrg != call.OrganizationID {
			return Outcome{}, task.ErrConflict
		}
		if status == "succeeded" {
			return Outcome{ID: id, Status: status, Summary: deref(summary), Output: output, ScopeVersion: scopeVersion}, nil
		}
		if status == "running" || status == "queued" {
			return Outcome{ID: id, Status: status}, ErrInProgress
		}
		if status == "blocked" || status == "failed" || status == "cancelled" {
			return Outcome{ID: id, Status: status, Summary: deref(summary)}, ErrToolBlocked
		}
	}
	if inserted.RowsAffected() == 1 {
		if _, err = d.Pool.Exec(ctx, `UPDATE tool_calls SET status='policy_check' WHERE id=$1 AND status='queued'`, id); err != nil {
			return Outcome{}, err
		}
	}
	if err := d.validateCall(ctx, call); err != nil {
		return d.block(ctx, call, id, "工具输入或 Schema 无效")
	}
	var enabled *bool
	err = d.Pool.QueryRow(ctx, `SELECT enabled FROM tool_bindings WHERE organization_id=$1 AND agent_role='worker' AND tool_name=$2`, call.OrganizationID, call.Name).Scan(&enabled)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Outcome{}, err
	}
	if enabled != nil && !*enabled {
		return d.block(ctx, call, id, "工具未绑定给 Worker")
	}
	if d.needsApproval(detail.Task.PolicyMode, call.Name) {
		var approvalStatus string
		err = d.Pool.QueryRow(ctx, `SELECT status FROM tool_approvals WHERE tool_call_id=$1`, id).Scan(&approvalStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			_, err = d.Pool.Exec(ctx, `INSERT INTO tool_approvals(id,tool_call_id,organization_id,requested_by,status) VALUES($1,$2,$3,$4,'pending')`, identity.NewID(), id, call.OrganizationID, call.ActorID)
			if err != nil {
				return Outcome{}, err
			}
			if err := d.audit(ctx, call, id, "tool.approval_requested", ""); err != nil {
				return Outcome{}, err
			}
			return Outcome{ID: id, Status: "policy_check", ScopeVersion: scopeVersion}, ErrApprovalRequired
		}
		if err != nil {
			return Outcome{}, err
		}
		if approvalStatus != "approved" {
			return Outcome{ID: id, Status: "policy_check", ScopeVersion: scopeVersion}, ErrApprovalRequired
		}
	}
	if detail.Task.ScopeVersion != scopeVersion {
		return d.block(ctx, call, id, "授权范围版本已改变")
	}
	if err := d.preflight(ctx, call, detail); err != nil {
		return d.blockPolicy(ctx, call, id, err)
	}
	tag, err := d.Pool.Exec(ctx, `UPDATE tool_calls SET status='running',run_id=$2,started_at=now() WHERE id=$1 AND status='policy_check'`, id, call.RunID)
	if err != nil {
		return Outcome{}, err
	}
	if tag.RowsAffected() != 1 {
		return Outcome{ID: id, Status: "running"}, ErrInProgress
	}
	if err := d.audit(ctx, call, id, "tool.policy_allowed", ""); err != nil {
		return Outcome{}, err
	}
	started := time.Now()
	var result Result
	var executeErr error
	switch call.Name {
	case "http.read":
		h := HTTPRead{Enforcer: d.Enforcer, Scopes: func(ctx context.Context) ([]task.ScopeEntry, int64, error) {
			current, err := (task.Store{Pool: d.Pool}).Get(ctx, call.OrganizationID, call.TaskID)
			return current.Scope, current.Task.ScopeVersion, err
		}}
		result, _, executeErr = h.Execute(ctx, call.Input)
	case "workspace.read", "workspace.write", "workspace.list":
		result, executeErr = (Workspace{Root: d.DataDir}).Execute(ctx, call.TaskID, call.IntentID, call.Name, call.Input)
	case "command.run":
		result, executeErr = (Command{Workspace: Workspace{Root: d.DataDir}, Image: d.CommandImage}).Execute(ctx, call.TaskID, call.IntentID, call.Input)
	case "skill.load":
		var input struct {
			Name string `json:"name"`
		}
		if executeErr = decodeStrict(call.Input, &input); executeErr == nil {
			var record skill.Record
			var body string
			record, body, executeErr = (skill.Store{Pool: d.Pool}).Load(ctx, call.OrganizationID, call.RunID, input.Name)
			if executeErr == nil {
				var unlocked []string
				unlocked, executeErr = d.AvailableForRun(ctx, call.OrganizationID, call.RunID)
				if executeErr == nil {
					result = Result{Summary: "已加载 " + record.Manifest.String(), Output: []byte(body + "\n\n已解锁工具：" + strings.Join(unlocked, ", ")), ContentType: "text/markdown"}
				}
			}
		}
	case "finding.report":
		var input finding.Input
		if executeErr = decodeStrict(call.Input, &input); executeErr == nil {
			input.TaskID, input.SourceIntentID, input.SourceRunID = call.TaskID, call.IntentID, call.RunID
			var item finding.Finding
			item, executeErr = (finding.Store{Pool: d.Pool}).Create(ctx, call.OrganizationID, call.ActorID, input)
			if executeErr == nil {
				payload, _ := json.Marshal(item)
				result = Result{Summary: "已创建或合并 Finding 草稿：" + item.Title, Output: payload, ContentType: "application/json"}
			}
		}
	default:
		if strings.HasPrefix(call.Name, "custom__") {
			result, executeErr = d.executeCustom(ctx, call)
		} else if server, name, ok := splitMCPName(call.Name); ok {
			var output []byte
			executeErr = d.enforceURLArguments(ctx, call, detail)
			if executeErr == nil {
				output, executeErr = (mcpclient.Store{Pool: d.Pool, Enforcer: d.Enforcer}).Call(ctx, call.OrganizationID, call.RunID, server, name, call.Input)
			}
			if executeErr == nil {
				result = Result{Summary: "MCP 工具 " + call.Name + " 已返回", Output: output, ContentType: "application/json"}
			}
		}
	}
	if executeErr != nil {
		if errors.Is(executeErr, policy.ErrOutOfScope) || errors.Is(executeErr, policy.ErrProtectedTarget) {
			return d.blockPolicy(ctx, call, id, executeErr)
		}
		_, _ = d.Pool.Exec(context.WithoutCancel(ctx), `UPDATE tool_calls SET status='failed',error_class='execution_failed',summary=$2,duration_ms=$3,completed_at=now() WHERE id=$1 AND status='running'`, id, executeErr.Error(), time.Since(started).Milliseconds())
		_ = d.audit(context.WithoutCancel(ctx), call, id, "tool.failed", executeErr.Error())
		return Outcome{ID: id, Status: "failed", Summary: executeErr.Error()}, executeErr
	}
	var latestScopeVersion int64
	if err := d.Pool.QueryRow(ctx, `SELECT scope_version FROM tasks WHERE id=$1`, call.TaskID).Scan(&latestScopeVersion); err != nil {
		return Outcome{}, err
	}
	if latestScopeVersion != scopeVersion {
		return d.block(ctx, call, id, "授权范围已更新，执行结果已丢弃")
	}
	if result.Exchange != nil {
		ex := result.Exchange
		_, err := (traffic.Store{Pool: d.Pool, Blobs: blob.Store{Root: d.DataDir}}).Record(ctx, traffic.RecordInput{OrganizationID: call.OrganizationID, TaskID: call.TaskID, RunID: call.RunID, IntentID: call.IntentID, ToolCallID: id, Method: ex.Method, URL: ex.URL, RemoteIP: ex.RemoteIP, StatusCode: ex.StatusCode, TLS: ex.TLS, RequestHeaders: ex.RequestHeaders, ResponseHeaders: ex.ResponseHeaders, RequestRaw: ex.RequestRaw, ResponseRaw: ex.ResponseRaw, RequestedAt: ex.RequestedAt, RespondedAt: ex.RespondedAt})
		if err != nil {
			_, _ = d.Pool.Exec(context.WithoutCancel(ctx), `UPDATE tool_calls SET status='failed',error_class='capture_failed',summary=$2,completed_at=now() WHERE id=$1 AND status='running'`, id, err.Error())
			return Outcome{ID: id, Status: "failed", Summary: err.Error()}, err
		}
	}
	blobHash, err := d.capture(result.Output)
	if err != nil {
		return Outcome{}, err
	}
	preview := result.Output
	if len(preview) > 16<<10 {
		preview = append(append([]byte{}, preview[:8<<10]...), preview[len(preview)-(8<<10):]...)
	}
	encoded, _ := json.Marshal(map[string]any{"preview": string(preview), "content_type": result.ContentType, "remote_ip": result.RemoteIP, "truncated": result.Truncated, "bytes": len(result.Output)})
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, err := d.Pool.Begin(settleCtx)
	if err != nil {
		return Outcome{}, err
	}
	defer tx.Rollback(settleCtx)
	if _, err = tx.Exec(settleCtx, `UPDATE tool_calls SET status='succeeded',summary=$2,output=$3,blob_hash=$4,exit_code=$5,duration_ms=$6,response_bytes=$7,completed_at=now() WHERE id=$1 AND status='running'`, id, result.Summary, encoded, blobHash, result.ExitCode, time.Since(started).Milliseconds(), len(result.Output)); err != nil {
		return Outcome{}, err
	}
	if _, err = tx.Exec(settleCtx, `UPDATE task_budgets SET tool_calls=tool_calls+1,updated_at=now() WHERE task_id=$1`, call.TaskID); err != nil {
		return Outcome{}, err
	}
	if _, err = tx.Exec(settleCtx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,$3,'tool.succeeded','tool_call',$4,'{}'::jsonb)`, identity.NewID(), call.OrganizationID, call.ActorID, id); err != nil {
		return Outcome{}, err
	}
	if err = tx.Commit(settleCtx); err != nil {
		return Outcome{}, err
	}
	return Outcome{ID: id, Status: "succeeded", Summary: result.Summary, Output: encoded, ScopeVersion: scopeVersion}, nil
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (d Dispatcher) needsApproval(mode, name string) bool {
	if mode == "approval" {
		return name == "workspace.write" || name == "finding.report" || name == "command.run" || strings.HasPrefix(name, "mcp__")
	}
	return mode == "graded" && (name == "command.run" || strings.HasPrefix(name, "mcp__"))
}

func (d Dispatcher) preflight(ctx context.Context, call Call, detail task.Detail) error {
	if call.Name == "finding.report" {
		var observed bool
		if err := d.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tool_calls WHERE run_id=$1 AND task_id=$2 AND intent_id=$3 AND name='http.read' AND status='succeeded')`, call.RunID, call.TaskID, call.IntentID).Scan(&observed); err != nil {
			return err
		}
		if !observed {
			return errors.New("上报漏洞前必须有同一 Run 的真实 HTTP 观察")
		}
	}
	var urlText string
	if call.Name == "http.read" {
		var input HTTPInput
		if err := decodeStrict(call.Input, &input); err != nil {
			return err
		}
		urlText = input.URL
	} else if strings.HasPrefix(call.Name, "custom__") {
		value, _, err := d.customURL(ctx, call)
		if err != nil {
			return err
		}
		urlText = value
	} else if _, _, ok := splitMCPName(call.Name); ok {
		return d.enforceURLArguments(ctx, call, detail)
	}
	if urlText != "" {
		_, err := d.Enforcer.Authorize(ctx, urlText, detail.Scope, detail.Task.ScopeVersion)
		return err
	}
	return nil
}

func (d Dispatcher) enforceURLArguments(ctx context.Context, call Call, detail task.Detail) error {
	var input any
	if err := json.Unmarshal(call.Input, &input); err != nil {
		return err
	}
	var check func(any) error
	check = func(value any) error {
		switch v := value.(type) {
		case string:
			if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
				_, err := d.Enforcer.Authorize(ctx, v, detail.Scope, detail.Task.ScopeVersion)
				return err
			}
		case []any:
			for _, item := range v {
				if err := check(item); err != nil {
					return err
				}
			}
		case map[string]any:
			for _, item := range v {
				if err := check(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return check(input)
}

func (d Dispatcher) block(ctx context.Context, call Call, id, reason string) (Outcome, error) {
	return d.blockDetailed(ctx, call, id, reason, map[string]any{"reason": reason, "tool": call.Name})
}

func (d Dispatcher) blockPolicy(ctx context.Context, call Call, id string, decisionErr error) (Outcome, error) {
	metadata := map[string]any{"reason": decisionErr.Error(), "tool": call.Name}
	if call.Name == "http.read" {
		var input HTTPInput
		if json.Unmarshal(call.Input, &input) == nil {
			if parsed, err := url.Parse(input.URL); err == nil {
				parsed.User, parsed.RawQuery, parsed.Fragment = nil, "", ""
				metadata["requested_target"] = parsed.String()
			}
		}
	}
	var denial *policy.Denial
	if errors.As(decisionErr, &denial) {
		metadata["blocked_target"] = denial.Target
		metadata["resolved_ips"] = denial.ResolvedIPs
		metadata["rule"] = denial.Rule
	}
	return d.blockDetailed(ctx, call, id, decisionErr.Error(), metadata)
}

func (d Dispatcher) blockDetailed(ctx context.Context, call Call, id, reason string, metadata map[string]any) (Outcome, error) {
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, err := d.Pool.Begin(settleCtx)
	if err != nil {
		return Outcome{}, err
	}
	defer tx.Rollback(settleCtx)
	if _, err = tx.Exec(settleCtx, `UPDATE tool_calls SET status='blocked',policy_reason=$2,summary=$2,completed_at=now() WHERE id=$1 AND status IN ('queued','policy_check','running')`, id, reason); err != nil {
		return Outcome{}, err
	}
	encoded, _ := json.Marshal(metadata)
	if _, err = tx.Exec(settleCtx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,$3,'tool.policy_blocked','tool_call',$4,$5)`, identity.NewID(), call.OrganizationID, call.ActorID, id, encoded); err != nil {
		return Outcome{}, err
	}
	if err = tx.Commit(settleCtx); err != nil {
		return Outcome{}, err
	}
	return Outcome{ID: id, Status: "blocked", Summary: reason}, ErrToolBlocked
}

func (d Dispatcher) audit(ctx context.Context, call Call, id, action, reason string) error {
	metadata, _ := json.Marshal(map[string]string{"reason": reason, "tool": call.Name})
	_, err := d.Pool.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,$3,$4,'tool_call',$5,$6)`, identity.NewID(), call.OrganizationID, call.ActorID, action, id, metadata)
	return err
}

func (d Dispatcher) capture(output []byte) (string, error) {
	if len(output) <= 16<<10 {
		return "", nil
	}
	if d.DataDir == "" {
		return "", errors.New("缺少输出存储目录")
	}
	sum := sha256.Sum256(output)
	hash := hex.EncodeToString(sum[:])
	dir := filepath.Join(d.DataDir, "tool-blobs", hash[:2])
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	filename := filepath.Join(dir, hash)
	if err := os.WriteFile(filename, output, 0600); err != nil {
		return "", err
	}
	return hash, nil
}

func (d Dispatcher) ReadOutput(ctx context.Context, orgID, id string) ([]byte, string, error) {
	var hash *string
	var output json.RawMessage
	err := d.Pool.QueryRow(ctx, `SELECT blob_hash,output FROM tool_calls WHERE id=$1 AND organization_id=$2 AND status='succeeded'`, id, orgID).Scan(&hash, &output)
	if err != nil {
		return nil, "", err
	}
	if hash == nil || *hash == "" {
		return output, "application/json", nil
	}
	if len(*hash) != 64 || strings.Trim(*hash, "0123456789abcdef") != "" {
		return nil, "", fmt.Errorf("invalid blob hash")
	}
	data, err := os.ReadFile(filepath.Join(d.DataDir, "tool-blobs", (*hash)[:2], *hash))
	return data, "application/octet-stream", err
}
