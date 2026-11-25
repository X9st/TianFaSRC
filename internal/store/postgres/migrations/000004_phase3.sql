ALTER TABLE task_intents DROP CONSTRAINT task_intents_status_check;
ALTER TABLE task_intents ADD CONSTRAINT task_intents_status_check CHECK (status IN ('open','leased','running','done','blocked','exhausted','paused','stopped'));
ALTER TABLE task_intents ADD COLUMN max_attempts INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts BETWEEN 1 AND 20);
ALTER TABLE task_intents ADD COLUMN observation_generation INTEGER NOT NULL DEFAULT 1 CHECK (observation_generation > 0);
ALTER TABLE task_intents ADD COLUMN ready_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE task_intents ADD COLUMN last_dispatched_at TIMESTAMPTZ;
ALTER TABLE task_intents ADD COLUMN stop_reason TEXT;
ALTER TABLE task_intents ADD COLUMN priority INTEGER NOT NULL DEFAULT 5 CHECK (priority BETWEEN 0 AND 10);
ALTER TABLE task_intents ADD COLUMN todo_id UUID;
CREATE INDEX task_intents_dispatch_idx ON task_intents(status,ready_at,created_at) WHERE status='open';
ALTER TABLE tasks ADD COLUMN last_worker_dispatch_at TIMESTAMPTZ;

ALTER TABLE agent_runs DROP CONSTRAINT agent_runs_status_check;
ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_status_check CHECK (status IN ('running','succeeded','blocked','failed','interrupted','paused','exhausted','stopped'));
ALTER TABLE agent_runs ADD COLUMN llm_profile_attempt INTEGER NOT NULL DEFAULT 0;

CREATE TABLE worker_leases (
  intent_id UUID PRIMARY KEY REFERENCES task_intents(id),
  task_id UUID NOT NULL REFERENCES tasks(id),
  run_id UUID NOT NULL UNIQUE REFERENCES agent_runs(id),
  owner_id UUID NOT NULL,
  fence BIGINT NOT NULL CHECK (fence > 0),
  attempt INTEGER NOT NULL CHECK (attempt > 0),
  heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX worker_leases_expiry_idx ON worker_leases(expires_at);

CREATE TABLE scheduler_capacity (
  singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
  max_workers INTEGER NOT NULL DEFAULT 32 CHECK (max_workers BETWEEN 1 AND 1000)
);
INSERT INTO scheduler_capacity(singleton,max_workers) VALUES(true,32);

CREATE TABLE planner_wakes (
  task_id UUID PRIMARY KEY REFERENCES tasks(id),
  due_at TIMESTAMPTZ NOT NULL,
  pending BOOLEAN NOT NULL DEFAULT true,
  owner_id UUID,
  fence BIGINT NOT NULL DEFAULT 0,
  expires_at TIMESTAMPTZ,
  reason TEXT NOT NULL,
  last_planned_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX planner_wakes_due_idx ON planner_wakes(due_at) WHERE pending;

CREATE TABLE worker_steers (
  id UUID PRIMARY KEY,
  task_id UUID NOT NULL REFERENCES tasks(id),
  intent_id UUID NOT NULL REFERENCES task_intents(id),
  actor_user_id UUID NOT NULL REFERENCES users(id),
  message TEXT NOT NULL CHECK (length(message) BETWEEN 1 AND 4000),
  source TEXT NOT NULL DEFAULT 'human' CHECK (source IN ('human','planner')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  consumed_run_id UUID REFERENCES agent_runs(id),
  consumed_at TIMESTAMPTZ
);
CREATE INDEX worker_steers_pending_idx ON worker_steers(intent_id,created_at) WHERE consumed_at IS NULL;

CREATE TABLE worker_observations (
  intent_id UUID NOT NULL REFERENCES task_intents(id),
  generation INTEGER NOT NULL CHECK (generation > 0),
  idempotency_key TEXT NOT NULL UNIQUE,
  state TEXT NOT NULL CHECK (state IN ('reserved','succeeded','failed')),
  summary TEXT,
  tool_call_id UUID,
  confirmed BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(intent_id,generation)
);

ALTER TABLE task_todos ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE task_todos ADD COLUMN detail TEXT NOT NULL DEFAULT '';
ALTER TABLE task_todos ADD COLUMN intent_id UUID REFERENCES task_intents(id);
ALTER TABLE task_todos ADD COLUMN planner_key TEXT;
CREATE UNIQUE INDEX task_todos_planner_key_idx ON task_todos(task_id,planner_key) WHERE planner_key IS NOT NULL;
ALTER TABLE task_intents ADD CONSTRAINT task_intents_todo_fk FOREIGN KEY (todo_id) REFERENCES task_todos(id);
CREATE INDEX task_todos_task_status_idx ON task_todos(task_id,status,created_at);

CREATE TABLE agent_context_digests (
  id UUID PRIMARY KEY,
  run_id UUID NOT NULL REFERENCES agent_runs(id),
  through_ordinal INTEGER NOT NULL CHECK (through_ordinal >= 0),
  summary TEXT NOT NULL,
  source_hash TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (run_id,through_ordinal)
);
