-- Forgejo integration: replaces the GitHub App integration (000019/000020).
-- Connections hold a per-workspace instance URL + encrypted token; repos carry
-- the webhook ku created so unlinking can remove it again. PRs link to issues
-- many-to-many so a PR body referencing several identifiers links them all.

CREATE TABLE forgejo_connections (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        UUID NOT NULL UNIQUE REFERENCES workspaces(id) ON DELETE CASCADE,
    instance_url        VARCHAR(512) NOT NULL,
    token               TEXT NOT NULL,
    account_login       VARCHAR(255) NOT NULL,
    account_avatar_url  TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE forgejo_repos (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    forgejo_repo_id     BIGINT NOT NULL,
    full_name           VARCHAR(512) NOT NULL,
    instance_url        VARCHAR(512) NOT NULL,
    default_branch      VARCHAR(255) NOT NULL DEFAULT 'main',
    webhook_id          BIGINT,
    webhook_secret      TEXT,
    is_active           BOOLEAN NOT NULL DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(workspace_id, forgejo_repo_id)
);

CREATE INDEX idx_forgejo_repos_workspace ON forgejo_repos(workspace_id);
CREATE UNIQUE INDEX idx_forgejo_repos_instance_full_name
    ON forgejo_repos(LOWER(instance_url), LOWER(full_name)) WHERE is_active;
ALTER TABLE forgejo_repos
    ADD CONSTRAINT forgejo_repos_workspace_id_id_key UNIQUE (workspace_id, id);

CREATE TABLE forgejo_pull_requests (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    forgejo_repo_id     UUID NOT NULL REFERENCES forgejo_repos(id) ON DELETE CASCADE,
    number              INT NOT NULL,
    title               VARCHAR(1024) NOT NULL,
    state               VARCHAR(20) NOT NULL CHECK (state IN ('open','closed','merged','draft')),
    checks_green        BOOLEAN NOT NULL DEFAULT false,
    author_login        VARCHAR(255) NOT NULL,
    author_avatar_url   TEXT,
    html_url            TEXT NOT NULL,
    head_branch         VARCHAR(512),
    head_sha            VARCHAR(64),
    base_branch         VARCHAR(512),
    additions           INT NOT NULL DEFAULT 0,
    deletions           INT NOT NULL DEFAULT 0,
    merged_at           TIMESTAMPTZ,
    closed_at           TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(forgejo_repo_id, number)
);

CREATE INDEX idx_forgejo_prs_workspace ON forgejo_pull_requests(workspace_id);
CREATE INDEX idx_forgejo_prs_head_sha ON forgejo_pull_requests(forgejo_repo_id, head_sha);

CREATE TABLE forgejo_pull_request_issues (
    pr_id               UUID NOT NULL REFERENCES forgejo_pull_requests(id) ON DELETE CASCADE,
    issue_id            UUID NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (pr_id, issue_id)
);

CREATE INDEX idx_forgejo_pr_issues_issue ON forgejo_pull_request_issues(issue_id);

CREATE TABLE forgejo_branches (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    issue_id            UUID REFERENCES issues(id) ON DELETE SET NULL,
    forgejo_repo_id     UUID NOT NULL REFERENCES forgejo_repos(id) ON DELETE CASCADE,
    name                VARCHAR(512) NOT NULL,
    html_url            TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(forgejo_repo_id, name)
);

CREATE INDEX idx_forgejo_branches_issue ON forgejo_branches(issue_id);

CREATE TABLE forgejo_commits (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    issue_id            UUID REFERENCES issues(id) ON DELETE SET NULL,
    forgejo_repo_id     UUID NOT NULL REFERENCES forgejo_repos(id) ON DELETE CASCADE,
    sha                 VARCHAR(64) NOT NULL,
    message             TEXT NOT NULL,
    author_login        VARCHAR(255),
    author_avatar_url   TEXT,
    html_url            TEXT NOT NULL,
    committed_at        TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(forgejo_repo_id, sha)
);

CREATE INDEX idx_forgejo_commits_issue ON forgejo_commits(issue_id);

-- Rules are workspace-level and may exist without a target ("disabled");
-- the slug is resolved against each issue's team at fire time.
CREATE TABLE forgejo_auto_transitions (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    event               VARCHAR(50) NOT NULL CHECK (event IN ('branch_created','pr_opened','pr_checks_green','pr_merged')),
    target_status       VARCHAR(50) NOT NULL DEFAULT '',
    target_status_id    UUID REFERENCES team_statuses(id) ON DELETE SET NULL,
    is_active           BOOLEAN NOT NULL DEFAULT true,
    UNIQUE(workspace_id, event)
);

-- Retarget Dev Machines from github_repos to forgejo_repos.
ALTER TABLE dev_machines DROP CONSTRAINT fk_dev_machines_repository;
ALTER TABLE dev_machine_scope_settings DROP CONSTRAINT fk_dev_machine_scope_settings_repository;
ALTER TABLE dev_machine_checkouts DROP CONSTRAINT fk_dev_machine_checkouts_repository;

ALTER TABLE dev_machine_scope_settings RENAME COLUMN github_repo_id TO forgejo_repo_id;
ALTER TABLE dev_machine_checkouts RENAME COLUMN github_repo_id TO forgejo_repo_id;

ALTER TABLE dev_machines
    ADD CONSTRAINT fk_dev_machines_repository FOREIGN KEY (workspace_id, repository_affinity_id)
        REFERENCES forgejo_repos(workspace_id, id) ON DELETE SET NULL (repository_affinity_id);
ALTER TABLE dev_machine_scope_settings
    ADD CONSTRAINT fk_dev_machine_scope_settings_repository FOREIGN KEY (workspace_id, forgejo_repo_id)
        REFERENCES forgejo_repos(workspace_id, id) ON DELETE SET NULL (forgejo_repo_id);
ALTER TABLE dev_machine_checkouts
    ADD CONSTRAINT fk_dev_machine_checkouts_repository FOREIGN KEY (workspace_id, forgejo_repo_id)
        REFERENCES forgejo_repos(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE dev_machines ALTER COLUMN repo_provider SET DEFAULT 'forgejo';
UPDATE dev_machines SET repo_provider = 'forgejo' WHERE repo_provider = 'github';

-- The GitHub integration is gone: drop its tables.
DROP TABLE IF EXISTS github_app_configs;
DROP TABLE IF EXISTS github_auto_transitions;
DROP TABLE IF EXISTS github_commits;
DROP TABLE IF EXISTS github_branches;
DROP TABLE IF EXISTS github_pull_requests;
DROP TABLE IF EXISTS github_repos;
DROP TABLE IF EXISTS github_installations;
