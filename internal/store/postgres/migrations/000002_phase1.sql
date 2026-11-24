CREATE TABLE organizations (
  id UUID PRIMARY KEY,
  name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
  singleton BOOLEAN NOT NULL DEFAULT true UNIQUE CHECK (singleton),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE users (
  id UUID PRIMARY KEY,
  organization_id UUID NOT NULL REFERENCES organizations(id),
  email TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('admin','operator','reviewer','viewer')),
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (organization_id, email)
);

CREATE TABLE sessions (
  id UUID PRIMARY KEY,
  user_id UUID NOT NULL REFERENCES users(id),
  token_hash BYTEA NOT NULL UNIQUE,
  csrf_hash BYTEA NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX sessions_active_user_idx ON sessions(user_id, expires_at) WHERE revoked_at IS NULL;

CREATE TABLE tasks (
  id UUID PRIMARY KEY,
  organization_id UUID NOT NULL REFERENCES organizations(id),
  title TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  description TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','queued','running','paused','completed','failed','cancelled')),
  policy_mode TEXT NOT NULL DEFAULT 'autonomous' CHECK (policy_mode IN ('autonomous','graded','approval')),
  worker_limit INTEGER NOT NULL DEFAULT 3 CHECK (worker_limit BETWEEN 1 AND 50),
  deadline_at TIMESTAMPTZ,
  budget JSONB NOT NULL DEFAULT '{"schema_version":1}'::jsonb,
  version BIGINT NOT NULL DEFAULT 1,
  scope_version BIGINT NOT NULL DEFAULT 1,
  created_by UUID NOT NULL REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ
);
CREATE INDEX tasks_org_created_idx ON tasks(organization_id, created_at DESC, id DESC) WHERE deleted_at IS NULL;

CREATE TABLE task_goals (
  id UUID PRIMARY KEY,
  task_id UUID NOT NULL REFERENCES tasks(id),
  text TEXT NOT NULL CHECK (length(text) BETWEEN 1 AND 4000),
  state TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open','met','abandoned')),
  priority INTEGER NOT NULL DEFAULT 5 CHECK (priority BETWEEN 0 AND 10),
  version BIGINT NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX task_goals_task_idx ON task_goals(task_id, priority DESC);

CREATE TABLE task_constraints (
  id UUID PRIMARY KEY,
  task_id UUID NOT NULL REFERENCES tasks(id),
  effect TEXT NOT NULL CHECK (effect IN ('allow','deny')),
  text TEXT NOT NULL CHECK (length(text) BETWEEN 1 AND 4000),
  source TEXT NOT NULL DEFAULT 'user',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE task_scopes (
  id UUID PRIMARY KEY,
  task_id UUID NOT NULL REFERENCES tasks(id),
  scope_version BIGINT NOT NULL,
  effect TEXT NOT NULL CHECK (effect IN ('allow','deny')),
  kind TEXT NOT NULL CHECK (kind IN ('domain','ip','cidr','url_prefix','service')),
  value TEXT NOT NULL,
  normalized_value TEXT NOT NULL,
  ports INTEGER[] NOT NULL DEFAULT '{}',
  path_prefix TEXT,
  created_by UUID NOT NULL REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (task_id, scope_version, effect, kind, normalized_value, ports)
);
CREATE INDEX task_scopes_current_idx ON task_scopes(task_id, scope_version);

CREATE TABLE task_todos (
  id UUID PRIMARY KEY,
  task_id UUID NOT NULL REFERENCES tasks(id),
  title TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','done','blocked')),
  depends_on UUID[],
  source_round UUID,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE task_budgets (
  task_id UUID PRIMARY KEY REFERENCES tasks(id),
  input_tokens BIGINT NOT NULL DEFAULT 0,
  output_tokens BIGINT NOT NULL DEFAULT 0,
  tool_calls BIGINT NOT NULL DEFAULT 0,
  cost_minor BIGINT NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE assets (
  id UUID PRIMARY KEY,
  organization_id UUID NOT NULL REFERENCES organizations(id),
  kind TEXT NOT NULL CHECK (kind IN ('company','root_domain','subdomain','ip','service','application','endpoint')),
  canonical_key TEXT NOT NULL,
  display_value TEXT NOT NULL,
  raw_value TEXT NOT NULL,
  parent_id UUID REFERENCES assets(id),
  attributes_json JSONB NOT NULL DEFAULT '{"schema_version":1}'::jsonb,
  first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  source_task_id UUID REFERENCES tasks(id),
  confidence REAL NOT NULL DEFAULT 1 CHECK (confidence BETWEEN 0 AND 1),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (organization_id, kind, canonical_key)
);
CREATE INDEX assets_org_kind_idx ON assets(organization_id, kind, canonical_key);

CREATE TABLE task_asset_links (
  task_id UUID NOT NULL REFERENCES tasks(id),
  asset_id UUID NOT NULL REFERENCES assets(id),
  linked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (task_id, asset_id)
);

CREATE TABLE exploration_nodes (
  id UUID PRIMARY KEY,
  task_id UUID NOT NULL REFERENCES tasks(id),
  kind TEXT NOT NULL CHECK (kind IN ('origin','goal','intent','fact','finding','hint','digest')),
  state TEXT NOT NULL,
  summary TEXT NOT NULL,
  source_intent_id UUID,
  run_id UUID,
  tool_call_id UUID,
  version BIGINT NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (id, task_id)
);
CREATE INDEX exploration_nodes_task_idx ON exploration_nodes(task_id, created_at, id);

CREATE TABLE exploration_edges (
  task_id UUID NOT NULL REFERENCES tasks(id),
  from_node_id UUID NOT NULL,
  to_node_id UUID NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('spawns','derived_from','yields','proves','covers')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (from_node_id, to_node_id, kind),
  CHECK (from_node_id <> to_node_id),
  FOREIGN KEY (from_node_id, task_id) REFERENCES exploration_nodes(id, task_id),
  FOREIGN KEY (to_node_id, task_id) REFERENCES exploration_nodes(id, task_id)
);
CREATE INDEX exploration_edges_to_idx ON exploration_edges(task_id, to_node_id);

CREATE TABLE exploration_anchors (
  node_id UUID NOT NULL REFERENCES exploration_nodes(id),
  asset_id UUID NOT NULL REFERENCES assets(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (node_id, asset_id)
);

CREATE TABLE domain_events (
  id UUID PRIMARY KEY,
  organization_id UUID NOT NULL REFERENCES organizations(id),
  task_id UUID REFERENCES tasks(id),
  event_type TEXT NOT NULL,
  payload JSONB NOT NULL,
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX domain_events_task_idx ON domain_events(task_id, occurred_at, id);

CREATE TABLE event_consumptions (
  consumer_name TEXT NOT NULL,
  event_id UUID NOT NULL REFERENCES domain_events(id),
  consumed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (consumer_name, event_id)
);

CREATE TABLE activities (
  id UUID PRIMARY KEY,
  event_id UUID NOT NULL UNIQUE REFERENCES domain_events(id),
  task_id UUID NOT NULL REFERENCES tasks(id),
  kind TEXT NOT NULL,
  summary TEXT NOT NULL,
  occurred_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX activities_task_time_idx ON activities(task_id, occurred_at DESC, id DESC);

CREATE TABLE audit_log (
  id UUID PRIMARY KEY,
  organization_id UUID REFERENCES organizations(id),
  actor_user_id UUID REFERENCES users(id),
  action TEXT NOT NULL,
  resource_type TEXT NOT NULL,
  resource_id UUID,
  detail JSONB NOT NULL DEFAULT '{"schema_version":1}'::jsonb,
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE OR REPLACE FUNCTION forbid_audit_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'audit_log is append-only';
END;
$$;
CREATE TRIGGER audit_log_append_only BEFORE UPDATE OR DELETE ON audit_log
FOR EACH ROW EXECUTE FUNCTION forbid_audit_mutation();
