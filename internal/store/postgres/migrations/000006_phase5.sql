CREATE TABLE blobs (
  sha256 TEXT PRIMARY KEY CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
  traffic_refs BIGINT NOT NULL DEFAULT 0 CHECK (traffic_refs >= 0),
  evidence_refs BIGINT NOT NULL DEFAULT 0 CHECK (evidence_refs >= 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  orphaned_at TIMESTAMPTZ
);

CREATE TABLE traffic (
  id UUID PRIMARY KEY,
  organization_id UUID NOT NULL REFERENCES organizations(id),
  task_id UUID NOT NULL REFERENCES tasks(id),
  run_id UUID REFERENCES agent_runs(id),
  intent_id UUID REFERENCES task_intents(id),
  tool_call_id UUID REFERENCES tool_calls(id),
  method TEXT NOT NULL,
  url TEXT NOT NULL,
  host TEXT NOT NULL,
  path TEXT NOT NULL,
  status_code INTEGER NOT NULL,
  remote_ip TEXT NOT NULL,
  tls JSONB NOT NULL DEFAULT '{}'::jsonb,
  request_headers JSONB NOT NULL DEFAULT '{}'::jsonb,
  response_headers JSONB NOT NULL DEFAULT '{}'::jsonb,
  request_sha256 TEXT REFERENCES blobs(sha256),
  response_sha256 TEXT REFERENCES blobs(sha256),
  request_bytes BIGINT NOT NULL DEFAULT 0,
  response_bytes BIGINT NOT NULL DEFAULT 0,
  request_body_bytes BIGINT NOT NULL DEFAULT 0,
  response_body_bytes BIGINT NOT NULL DEFAULT 0,
  request_body_sha256 TEXT,
  response_body_sha256 TEXT,
  requested_at TIMESTAMPTZ NOT NULL,
  responded_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX traffic_task_time_idx ON traffic(task_id,requested_at DESC,id DESC);
CREATE INDEX traffic_filter_idx ON traffic(organization_id,host,status_code,requested_at DESC);
CREATE INDEX traffic_tool_call_idx ON traffic(tool_call_id);

CREATE TABLE proxy_tokens (
  id UUID PRIMARY KEY,
  organization_id UUID NOT NULL REFERENCES organizations(id),
  task_id UUID NOT NULL REFERENCES tasks(id),
  created_by UUID NOT NULL REFERENCES users(id),
  token_hash BYTEA NOT NULL UNIQUE,
  expires_at TIMESTAMPTZ NOT NULL,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX proxy_tokens_task_idx ON proxy_tokens(task_id,expires_at) WHERE revoked_at IS NULL;

CREATE TABLE findings (
  id UUID PRIMARY KEY,
  organization_id UUID NOT NULL REFERENCES organizations(id),
  task_id UUID NOT NULL REFERENCES tasks(id),
  node_id UUID NOT NULL REFERENCES exploration_nodes(id),
  fingerprint TEXT NOT NULL,
  title TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  vulnerability_type TEXT NOT NULL CHECK (length(vulnerability_type) BETWEEN 1 AND 120),
  location TEXT NOT NULL,
  severity TEXT NOT NULL CHECK (severity IN ('info','low','medium','high','critical')),
  cvss JSONB,
  status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','confirmed','fixed')),
  preconditions TEXT NOT NULL DEFAULT '',
  impact TEXT NOT NULL DEFAULT '',
  reproduction_steps JSONB NOT NULL DEFAULT '[]'::jsonb,
  observed_result TEXT NOT NULL DEFAULT '',
  recommendation TEXT NOT NULL DEFAULT '',
  source_intent_id UUID REFERENCES task_intents(id),
  evidence_version BIGINT NOT NULL DEFAULT 0,
  report_evidence_version BIGINT NOT NULL DEFAULT 0,
  version BIGINT NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(task_id,fingerprint)
);
CREATE INDEX findings_task_status_idx ON findings(task_id,status,severity);

CREATE TABLE finding_assets (
  finding_id UUID NOT NULL REFERENCES findings(id) ON DELETE CASCADE,
  asset_id UUID NOT NULL REFERENCES assets(id),
  PRIMARY KEY(finding_id,asset_id)
);
CREATE TABLE finding_sources (
  finding_id UUID NOT NULL REFERENCES findings(id) ON DELETE CASCADE,
  run_id UUID NOT NULL REFERENCES agent_runs(id),
  PRIMARY KEY(finding_id,run_id)
);

CREATE TABLE evidence_snapshots (
  id UUID PRIMARY KEY,
  organization_id UUID NOT NULL REFERENCES organizations(id),
  task_id UUID NOT NULL REFERENCES tasks(id),
  source_traffic_id UUID,
  metadata JSONB NOT NULL,
  request_headers JSONB NOT NULL,
  response_headers JSONB NOT NULL,
  request_sha256 TEXT REFERENCES blobs(sha256),
  response_sha256 TEXT REFERENCES blobs(sha256),
  captured_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(task_id,source_traffic_id)
);
CREATE TABLE evidence_bindings (
  id UUID PRIMARY KEY,
  finding_id UUID NOT NULL REFERENCES findings(id) ON DELETE CASCADE,
  snapshot_id UUID NOT NULL REFERENCES evidence_snapshots(id),
  role TEXT NOT NULL CHECK (role IN ('baseline','proof','verification','supporting')),
  note TEXT NOT NULL DEFAULT '',
  position INTEGER NOT NULL CHECK (position >= 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(finding_id,snapshot_id),
  UNIQUE(finding_id,position)
);

CREATE OR REPLACE FUNCTION forbid_evidence_snapshot_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'evidence_snapshots are immutable';
END;
$$;
CREATE TRIGGER evidence_snapshot_immutable BEFORE UPDATE ON evidence_snapshots
FOR EACH ROW EXECUTE FUNCTION forbid_evidence_snapshot_mutation();

CREATE TABLE retests (
  id UUID PRIMARY KEY,
  finding_id UUID NOT NULL REFERENCES findings(id),
  organization_id UUID NOT NULL REFERENCES organizations(id),
  task_id UUID NOT NULL REFERENCES tasks(id),
  evidence_version BIGINT NOT NULL,
  scope_version BIGINT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','completed','failed')),
  conclusion TEXT CHECK (conclusion IN ('still_vulnerable','fixed','inconclusive')),
  note TEXT NOT NULL DEFAULT '',
  created_by UUID NOT NULL REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ
);
CREATE TABLE retest_evidence (
  retest_id UUID NOT NULL REFERENCES retests(id),
  snapshot_id UUID NOT NULL REFERENCES evidence_snapshots(id),
  PRIMARY KEY(retest_id,snapshot_id)
);

CREATE TABLE task_reports (
  task_id UUID PRIMARY KEY REFERENCES tasks(id),
  organization_id UUID NOT NULL REFERENCES organizations(id),
  body TEXT NOT NULL DEFAULT '',
  version BIGINT NOT NULL DEFAULT 1,
  evidence_version BIGINT NOT NULL DEFAULT 0,
  updated_by UUID REFERENCES users(id),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
