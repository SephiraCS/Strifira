-- Best-effort reverse of 000040: restores the GitHub schema and re-points
-- Dev Machines at it. Data written under the Forgejo tables is not converted
-- back (the GitHub integration is removed from the application).

ALTER TABLE dev_machines DROP CONSTRAINT fk_dev_machines_repository;
ALTER TABLE dev_machine_scope_settings DROP CONSTRAINT fk_dev_machine_scope_settings_repository;
ALTER TABLE dev_machine_checkouts DROP CONSTRAINT fk_dev_machine_checkouts_repository;

DROP TABLE IF EXISTS forgejo_auto_transitions;
DROP TABLE IF EXISTS forgejo_commits;
DROP TABLE IF EXISTS forgejo_branches;
DROP TABLE IF EXISTS forgejo_pull_request_issues;
DROP TABLE IF EXISTS forgejo_pull_requests;
DROP TABLE IF EXISTS forgejo_repos;
DROP TABLE IF EXISTS forgejo_connections;

CREATE TABLE github_installations (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        UUID NOT NULL UNIQUE REFERENCES workspaces(id) ON DELETE CASCADE,
    installation_id     BIGINT NOT NULL UNIQUE,
    account_login       VARCHAR(255) NOT NULL,
    account_type        VARCHAR(20) NOT NULL CHECK (account_type IN ('Organization','User')),
    access_token        TEXT,
    token_expires_at    TIMESTAMPTZ,
    installed_by        UUID NOT NULL REFERENCES users(id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE github_repos (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    installation_id     UUID NOT NULL REFERENCES github_installations(id) ON DELETE CASCADE,
    workspace_id        UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    github_repo_id      BIGINT NOT NULL,
    full_name           VARCHAR(512) NOT NULL,
    default_branch      VARCHAR(255) NOT NULL DEFAULT 'main',
    is_active           BOOLEAN NOT NULL DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(workspace_id, github_repo_id)
);
CREATE INDEX idx_github_repos_workspace ON github_repos(workspace_id);
ALTER TABLE github_repos
    ADD CONSTRAINT github_repos_workspace_id_id_key UNIQUE (workspace_id, id);

ALTER TABLE dev_machines
    ADD CONSTRAINT fk_dev_machines_repository FOREIGN KEY (workspace_id, repository_affinity_id)
        REFERENCES github_repos(workspace_id, id) ON DELETE SET NULL (repository_affinity_id);
ALTER TABLE dev_machine_scope_settings
    RENAME COLUMN forgejo_repo_id TO github_repo_id;
ALTER TABLE dev_machine_scope_settings
    ADD CONSTRAINT fk_dev_machine_scope_settings_repository FOREIGN KEY (workspace_id, github_repo_id)
        REFERENCES github_repos(workspace_id, id) ON DELETE SET NULL (github_repo_id);
ALTER TABLE dev_machine_checkouts
    RENAME COLUMN forgejo_repo_id TO github_repo_id;
ALTER TABLE dev_machine_checkouts
    ADD CONSTRAINT fk_dev_machine_checkouts_repository FOREIGN KEY (workspace_id, github_repo_id)
        REFERENCES github_repos(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE dev_machines ALTER COLUMN repo_provider SET DEFAULT 'github';
