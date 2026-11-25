CREATE TABLE secret_values (
  id UUID PRIMARY KEY,
  organization_id UUID NOT NULL REFERENCES organizations(id),
  kind TEXT NOT NULL CHECK (kind IN ('llm_api_key')),
  ciphertext BYTEA NOT NULL,
  nonce BYTEA NOT NULL CHECK (octet_length(nonce) = 12),
  hint TEXT NOT NULL CHECK (length(hint) <= 8),
  key_version INTEGER NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (id, organization_id)
);

CREATE TABLE llm_profiles (
  id UUID PRIMARY KEY,
  organization_id UUID NOT NULL REFERENCES organizations(id),
  name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  format TEXT NOT NULL CHECK (format IN ('openai-chat','openai-responses','anthropic')),
  base_url TEXT NOT NULL,
  base_url_host TEXT NOT NULL,
  model TEXT NOT NULL CHECK (length(model) BETWEEN 1 AND 200),
  credential_ref UUID REFERENCES secret_values(id),
  priority INTEGER NOT NULL DEFAULT 0,
  enabled BOOLEAN NOT NULL DEFAULT true,
  stream BOOLEAN NOT NULL DEFAULT true,
  context_window INTEGER NOT NULL CHECK (context_window BETWEEN 1024 AND 2000000),
  max_output_tokens INTEGER NOT NULL CHECK (max_output_tokens BETWEEN 1 AND 1000000),
  rate_per_second INTEGER NOT NULL DEFAULT 2 CHECK (rate_per_second BETWEEN 1 AND 10000),
  rate_per_minute INTEGER NOT NULL DEFAULT 60 CHECK (rate_per_minute BETWEEN 1 AND 1000000),
  thinking_mode TEXT NOT NULL DEFAULT 'auto',
  reasoning_effort TEXT NOT NULL DEFAULT '',
  session_header_name TEXT NOT NULL DEFAULT '',
  retry_policy JSONB NOT NULL DEFAULT '{"schema_version":1}'::jsonb,
  circuit_state TEXT NOT NULL DEFAULT 'closed' CHECK (circuit_state IN ('closed','open','half_open')),
  health_state TEXT NOT NULL DEFAULT 'healthy' CHECK (health_state IN ('healthy','degraded','open_circuit','half_open','disabled')),
  soft_failures INTEGER NOT NULL DEFAULT 0,
  cooldown_until TIMESTAMPTZ,
  probe_lease_until TIMESTAMPTZ,
  last_success_at TIMESTAMPTZ,
  last_failure_at TIMESTAMPTZ,
  second_window_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  second_used INTEGER NOT NULL DEFAULT 0,
  minute_window_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  minute_used INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (organization_id, name),
  FOREIGN KEY (credential_ref, organization_id) REFERENCES secret_values(id, organization_id)
);
CREATE INDEX llm_profiles_candidates_idx ON llm_profiles(organization_id, enabled, priority DESC, id);

CREATE TABLE llm_default_chain (
  organization_id UUID NOT NULL REFERENCES organizations(id),
  position INTEGER NOT NULL CHECK (position >= 0),
  profile_id UUID NOT NULL REFERENCES llm_profiles(id),
  PRIMARY KEY (organization_id, position),
  UNIQUE (organization_id, profile_id)
);

CREATE TABLE task_llm_chain (
  task_id UUID NOT NULL REFERENCES tasks(id),
  position INTEGER NOT NULL CHECK (position >= 0),
  profile_id UUID NOT NULL REFERENCES llm_profiles(id),
  PRIMARY KEY (task_id, position),
  UNIQUE (task_id, profile_id)
);

CREATE TABLE task_agent_profile_routes (
  task_id UUID NOT NULL REFERENCES tasks(id),
  role TEXT NOT NULL CHECK (role IN ('decomposer','planner','worker')),
  position INTEGER NOT NULL CHECK (position >= 0),
  profile_id UUID NOT NULL REFERENCES llm_profiles(id),
  PRIMARY KEY (task_id, role, position),
  UNIQUE (task_id, role, profile_id)
);

CREATE TABLE prompt_templates (
  id UUID PRIMARY KEY,
  organization_id UUID NOT NULL REFERENCES organizations(id),
  role TEXT NOT NULL CHECK (role IN ('decomposer','planner','worker')),
  version INTEGER NOT NULL CHECK (version >= 1),
  body TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 30000),
  content_hash TEXT NOT NULL,
  created_by UUID NOT NULL REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (organization_id, role, version)
);
CREATE TABLE active_prompt_templates (
  organization_id UUID NOT NULL REFERENCES organizations(id),
  role TEXT NOT NULL CHECK (role IN ('decomposer','planner','worker')),
  version INTEGER NOT NULL,
  PRIMARY KEY (organization_id, role),
  FOREIGN KEY (organization_id, role, version) REFERENCES prompt_templates(organization_id, role, version)
);

CREATE TABLE agent_runs (
  id UUID PRIMARY KEY,
  organization_id UUID NOT NULL REFERENCES organizations(id),
  task_id UUID NOT NULL REFERENCES tasks(id),
  intent_id UUID,
  role TEXT NOT NULL CHECK (role IN ('decomposer','planner','worker')),
  status TEXT NOT NULL CHECK (status IN ('running','succeeded','blocked','failed')),
  prompt_version INTEGER NOT NULL,
  prompt_hash TEXT NOT NULL,
  session_key TEXT NOT NULL,
  profile_id UUID REFERENCES llm_profiles(id),
  attempt INTEGER NOT NULL DEFAULT 1,
  error_class TEXT,
  blocked_until TIMESTAMPTZ,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ,
  FOREIGN KEY (intent_id) REFERENCES exploration_nodes(id)
);
CREATE INDEX agent_runs_task_idx ON agent_runs(task_id, started_at DESC, id DESC);

CREATE TABLE agent_messages (
  id UUID PRIMARY KEY,
  run_id UUID NOT NULL REFERENCES agent_runs(id),
  ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
  role TEXT NOT NULL CHECK (role IN ('system','user','assistant','tool')),
  trust_level TEXT NOT NULL CHECK (trust_level IN ('trusted','untrusted_observation')),
  content TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (run_id, ordinal)
);

CREATE TABLE llm_attempts (
  id UUID PRIMARY KEY,
  run_id UUID NOT NULL REFERENCES agent_runs(id),
  profile_id UUID NOT NULL REFERENCES llm_profiles(id),
  ordinal INTEGER NOT NULL CHECK (ordinal >= 1),
  status TEXT NOT NULL CHECK (status IN ('running','succeeded','failed','partial')),
  error_class TEXT,
  delivered_text TEXT NOT NULL DEFAULT '',
  input_tokens BIGINT NOT NULL DEFAULT 0,
  output_tokens BIGINT NOT NULL DEFAULT 0,
  cache_read_tokens BIGINT NOT NULL DEFAULT 0,
  cache_write_tokens BIGINT NOT NULL DEFAULT 0,
  reasoning_tokens BIGINT NOT NULL DEFAULT 0,
  estimated BOOLEAN NOT NULL DEFAULT false,
  latency_ms BIGINT,
  first_token_ms BIGINT,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ,
  UNIQUE (run_id, ordinal)
);
CREATE INDEX llm_attempts_profile_time_idx ON llm_attempts(profile_id, started_at DESC);

CREATE TABLE task_intents (
  id UUID PRIMARY KEY REFERENCES exploration_nodes(id),
  task_id UUID NOT NULL REFERENCES tasks(id),
  goal_id UUID NOT NULL REFERENCES task_goals(id),
  objective TEXT NOT NULL,
  success_criteria TEXT NOT NULL,
  dedup_key TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','running','done','blocked','exhausted','paused','stopped')),
  attempt INTEGER NOT NULL DEFAULT 0,
  blocking_reason TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (task_id, dedup_key)
);
CREATE INDEX task_intents_active_idx ON task_intents(task_id, status, created_at);

CREATE TABLE goal_proofs (
  goal_id UUID NOT NULL REFERENCES task_goals(id),
  evidence_node_id UUID NOT NULL REFERENCES exploration_nodes(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (goal_id, evidence_node_id)
);
