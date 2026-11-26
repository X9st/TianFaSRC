-- User-defined task budgets were removed. Keep task_budgets as historical usage
-- counters; no user-facing limits are read from it after this migration.
ALTER TABLE tasks DROP COLUMN budget;

CREATE TABLE tool_calls (
  id UUID PRIMARY KEY,
  organization_id UUID NOT NULL REFERENCES organizations(id),
  task_id UUID NOT NULL REFERENCES tasks(id),
  intent_id UUID NOT NULL REFERENCES task_intents(id),
  run_id UUID NOT NULL REFERENCES agent_runs(id),
  agent_key TEXT NOT NULL,
  name TEXT NOT NULL,
  input JSONB NOT NULL,
  idempotency_key TEXT NOT NULL UNIQUE,
  scope_version BIGINT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('queued','policy_check','running','succeeded','failed','blocked','cancelled')),
  policy_reason TEXT,
  output JSONB,
  summary TEXT,
  blob_hash TEXT,
  exit_code INTEGER,
  duration_ms BIGINT,
  response_bytes BIGINT,
  error_class TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  started_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ
);
CREATE INDEX tool_calls_task_time_idx ON tool_calls(task_id,created_at DESC,id DESC);
CREATE INDEX tool_calls_run_idx ON tool_calls(run_id,created_at,id);

CREATE TABLE tool_approvals (
  id UUID PRIMARY KEY,
  tool_call_id UUID NOT NULL UNIQUE REFERENCES tool_calls(id),
  organization_id UUID NOT NULL REFERENCES organizations(id),
  requested_by UUID REFERENCES users(id),
  status TEXT NOT NULL CHECK (status IN ('pending','approved','rejected')),
  decided_by UUID REFERENCES users(id),
  reason TEXT,
  requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_at TIMESTAMPTZ
);

CREATE TABLE tool_bindings (
  organization_id UUID NOT NULL REFERENCES organizations(id),
  agent_role TEXT NOT NULL CHECK (agent_role IN ('planner','worker','decomposer')),
  tool_name TEXT NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT true,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(organization_id,agent_role,tool_name)
);

CREATE TABLE skills (
  id UUID PRIMARY KEY,
  organization_id UUID NOT NULL REFERENCES organizations(id),
  name TEXT NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9_-]{0,79}$'),
  version TEXT NOT NULL CHECK (length(version) BETWEEN 1 AND 80),
  description TEXT NOT NULL CHECK (length(description) BETWEEN 1 AND 1000),
  body TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  source TEXT NOT NULL,
  agents TEXT[] NOT NULL DEFAULT '{}',
  capabilities TEXT[] NOT NULL DEFAULT '{}',
  mcps TEXT[] NOT NULL DEFAULT '{}',
  enabled BOOLEAN NOT NULL DEFAULT true,
  installed_by UUID NOT NULL REFERENCES users(id),
  installed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(organization_id,name,version)
);
CREATE INDEX skills_enabled_idx ON skills(organization_id,enabled,name,version);

CREATE TABLE run_loaded_skills (
  run_id UUID NOT NULL REFERENCES agent_runs(id),
  skill_id UUID NOT NULL REFERENCES skills(id),
  loaded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(run_id,skill_id)
);

CREATE TABLE mcp_servers (
  id UUID PRIMARY KEY,
  organization_id UUID NOT NULL REFERENCES organizations(id),
  name TEXT NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9_-]{0,79}$'),
  transport TEXT NOT NULL CHECK (transport IN ('stdio','streamable_http')),
  endpoint TEXT,
  command TEXT,
  args JSONB NOT NULL DEFAULT '[]'::jsonb,
  enabled BOOLEAN NOT NULL DEFAULT false,
  created_by UUID NOT NULL REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(organization_id,name)
);

CREATE TABLE mcp_tools (
  server_id UUID NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  input_schema JSONB NOT NULL,
  schema_hash TEXT NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT false,
  PRIMARY KEY(server_id,name)
);

CREATE TABLE custom_http_tools (
  id UUID PRIMARY KEY,
  organization_id UUID NOT NULL REFERENCES organizations(id),
  name TEXT NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9_-]{0,79}$'),
  description TEXT NOT NULL DEFAULT '',
  url_template TEXT NOT NULL,
  method TEXT NOT NULL CHECK (method IN ('GET','HEAD')),
  input_schema JSONB NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT false,
  created_by UUID NOT NULL REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(organization_id,name)
);
