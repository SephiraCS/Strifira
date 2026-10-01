-- Outbound workers are tied to a current workspace member, independent of the
-- optional Docker machine manager.
CREATE TABLE dev_boxes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    created_by_user_id UUID NOT NULL,
    name VARCHAR(100) NOT NULL CHECK (length(btrim(name)) > 0),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    token_hash VARCHAR(64) NOT NULL UNIQUE CHECK (length(token_hash) = 64),
    token_prefix VARCHAR(32) NOT NULL,
    hostname VARCHAR(255) NOT NULL DEFAULT '',
    os VARCHAR(64) NOT NULL DEFAULT '',
    arch VARCHAR(64) NOT NULL DEFAULT '',
    version VARCHAR(100) NOT NULL DEFAULT '',
    providers JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(providers) = 'array'),
    last_seen_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    FOREIGN KEY (workspace_id, created_by_user_id)
        REFERENCES workspace_members(workspace_id, user_id) ON DELETE CASCADE,
    UNIQUE (workspace_id, id, created_by_user_id)
);

CREATE UNIQUE INDEX dev_boxes_creator_name_key
    ON dev_boxes (workspace_id, created_by_user_id, lower(name)) WHERE revoked_at IS NULL;

CREATE TABLE dev_box_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    box_id UUID NOT NULL,
    requested_by_user_id UUID NOT NULL,
    issue_id UUID NOT NULL,
    issue_identifier VARCHAR(100) NOT NULL,
    forgejo_repo_id UUID NOT NULL,
    provider VARCHAR(32) NOT NULL CHECK (provider IN ('claude-code', 'codex', 'opencode')),
    prompt TEXT NOT NULL CHECK (octet_length(prompt) <= 200000),
    repository_url TEXT NOT NULL,
    repository_full_name VARCHAR(500) NOT NULL,
    base_branch VARCHAR(255) NOT NULL,
    working_branch VARCHAR(255) NOT NULL,
    test_command JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(test_command) = 'array'),
    acceptance_criteria JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(acceptance_criteria) = 'array'),
    max_runtime_seconds INTEGER NOT NULL CHECK (max_runtime_seconds BETWEEN 30 AND 86400),
    status VARCHAR(20) NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'leased', 'running', 'succeeded', 'failed', 'cancelled', 'timeout')),
    cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
    lease_hash VARCHAR(64) NOT NULL DEFAULT '',
    lease_expires_at TIMESTAMPTZ,
    deadline_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    result JSONB CHECK (result IS NULL OR (jsonb_typeof(result) = 'object' AND octet_length(result::text) <= 2097152)),
    logs_bytes BIGINT NOT NULL DEFAULT 0 CHECK (logs_bytes BETWEEN 0 AND 4194304),
    logs_truncated BOOLEAN NOT NULL DEFAULT FALSE,
    FOREIGN KEY (workspace_id, box_id, requested_by_user_id)
        REFERENCES dev_boxes(workspace_id, id, created_by_user_id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, issue_id)
        REFERENCES issues(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, forgejo_repo_id)
        REFERENCES forgejo_repos(workspace_id, id) ON DELETE CASCADE,
    UNIQUE (box_id, working_branch),
    CHECK (status NOT IN ('leased', 'running') OR (lease_hash <> '' AND lease_expires_at IS NOT NULL)),
    CHECK (status <> 'running' OR (started_at IS NOT NULL AND deadline_at IS NOT NULL))
);

CREATE UNIQUE INDEX dev_box_jobs_one_active_per_box
    ON dev_box_jobs (box_id) WHERE status IN ('leased', 'running');
CREATE INDEX dev_box_jobs_queue
    ON dev_box_jobs (box_id, created_at, id) WHERE status = 'queued';
CREATE INDEX dev_box_jobs_creator_issue
    ON dev_box_jobs (workspace_id, requested_by_user_id, issue_id, created_at DESC);
CREATE INDEX dev_box_jobs_creator_box
    ON dev_box_jobs (workspace_id, requested_by_user_id, box_id, created_at DESC);
CREATE INDEX dev_box_jobs_expiring
    ON dev_box_jobs (lease_expires_at) WHERE status IN ('leased', 'running');

CREATE TABLE dev_box_job_logs (
    id BIGSERIAL PRIMARY KEY,
    job_id UUID NOT NULL REFERENCES dev_box_jobs(id) ON DELETE CASCADE,
    sequence BIGINT NOT NULL CHECK (sequence > 0),
    stream VARCHAR(10) NOT NULL CHECK (stream IN ('stdout', 'stderr', 'system')),
    content TEXT NOT NULL CHECK (octet_length(content) <= 65536),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (job_id, sequence)
);
CREATE INDEX dev_box_job_logs_cursor ON dev_box_job_logs (job_id, id);
