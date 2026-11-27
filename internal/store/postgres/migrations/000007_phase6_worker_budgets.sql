-- Worker budgets are internal Planner decisions. The task API never accepts
-- user supplied budget limits; task_budgets remains aggregate usage only.
ALTER TABLE task_intents
  ADD COLUMN budget_model_rounds INTEGER NOT NULL DEFAULT 4 CHECK (budget_model_rounds BETWEEN 1 AND 8),
  ADD COLUMN budget_tool_calls INTEGER NOT NULL DEFAULT 4 CHECK (budget_tool_calls BETWEEN 1 AND 12),
  ADD COLUMN budget_tokens INTEGER NOT NULL DEFAULT 12000 CHECK (budget_tokens BETWEEN 1000 AND 30000),
  ADD COLUMN budget_seconds INTEGER NOT NULL DEFAULT 90 CHECK (budget_seconds BETWEEN 15 AND 180),
  ADD COLUMN budget_reason TEXT NOT NULL DEFAULT '系统保守默认值' CHECK (length(budget_reason) BETWEEN 1 AND 500);

CREATE TABLE worker_run_budgets (
  run_id UUID PRIMARY KEY REFERENCES agent_runs(id),
  organization_id UUID NOT NULL REFERENCES organizations(id),
  task_id UUID NOT NULL REFERENCES tasks(id),
  intent_id UUID NOT NULL REFERENCES task_intents(id),
  max_model_rounds INTEGER NOT NULL CHECK (max_model_rounds BETWEEN 1 AND 8),
  max_tool_calls INTEGER NOT NULL CHECK (max_tool_calls BETWEEN 1 AND 12),
  max_tokens INTEGER NOT NULL CHECK (max_tokens BETWEEN 1000 AND 30000),
  max_seconds INTEGER NOT NULL CHECK (max_seconds BETWEEN 15 AND 180),
  model_rounds_used INTEGER NOT NULL DEFAULT 0 CHECK (model_rounds_used >= 0),
  tool_calls_used INTEGER NOT NULL DEFAULT 0 CHECK (tool_calls_used >= 0),
  input_tokens_used BIGINT NOT NULL DEFAULT 0 CHECK (input_tokens_used >= 0),
  output_tokens_used BIGINT NOT NULL DEFAULT 0 CHECK (output_tokens_used >= 0),
  reason TEXT NOT NULL,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX worker_run_budgets_task_idx ON worker_run_budgets(task_id,started_at DESC);

INSERT INTO worker_run_budgets(run_id,organization_id,task_id,intent_id,max_model_rounds,max_tool_calls,max_tokens,max_seconds,reason,started_at)
SELECT r.id,r.organization_id,r.task_id,r.intent_id,i.budget_model_rounds,i.budget_tool_calls,i.budget_tokens,i.budget_seconds,'迁移时运行中的 Worker',r.started_at
FROM agent_runs r JOIN task_intents i ON i.id=r.intent_id
WHERE r.role='worker' AND r.status='running';
