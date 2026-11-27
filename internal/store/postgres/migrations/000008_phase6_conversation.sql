CREATE TABLE task_conversation (
  id UUID PRIMARY KEY,
  organization_id UUID NOT NULL REFERENCES organizations(id),
  task_id UUID NOT NULL REFERENCES tasks(id),
  actor_user_id UUID REFERENCES users(id),
  role TEXT NOT NULL CHECK (role IN ('human','system')),
  action TEXT NOT NULL CHECK (action IN ('ask','goal','constraint','intent','steer')),
  content TEXT NOT NULL CHECK (length(content) BETWEEN 1 AND 4000),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX task_conversation_task_idx ON task_conversation(task_id,created_at DESC,id DESC);
