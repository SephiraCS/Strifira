-- GitHub migration: rename the Forgejo integration tables to GitHub. The
-- Forgejo integration replaced an earlier GitHub App integration in 000042
-- (dropping its tables), so the github_* names are free again. Data is
-- preserved: PR, branch and commit history stays visible, and old connection
-- rows keep their encrypted tokens — the encryption key label predates this
-- migration and is unchanged, so tokens decrypt but simply fail live
-- validation until a workspace reconnects with a GitHub token.

ALTER TABLE forgejo_connections RENAME TO github_connections;
ALTER TABLE forgejo_repos RENAME TO github_repos;
ALTER TABLE forgejo_repos RENAME COLUMN forgejo_repo_id TO github_repo_id;
ALTER TABLE forgejo_pull_requests RENAME TO github_pull_requests;
ALTER TABLE forgejo_pull_request_issues RENAME TO github_pull_request_issues;
ALTER TABLE forgejo_branches RENAME TO github_branches;
ALTER TABLE forgejo_commits RENAME TO github_commits;
ALTER TABLE forgejo_auto_transitions RENAME TO github_auto_transitions;
ALTER TABLE forgejo_auto_transitions RENAME COLUMN assign_forgejo_user TO assign_github_user;
ALTER TABLE forgejo_user_mappings RENAME TO github_user_mappings;
ALTER TABLE forgejo_user_mappings RENAME COLUMN forgejo_login TO github_login;
ALTER TABLE forgejo_workspace_disables RENAME TO github_workspace_disables;

-- Forgejo-named columns on dev-machine tables (created by 000042/000045/
-- 000046/000050) follow the integration rename so the repository layer's
-- github_* identifiers resolve.
ALTER TABLE dev_machine_scope_settings RENAME COLUMN forgejo_repo_id TO github_repo_id;
ALTER TABLE dev_machine_checkouts RENAME COLUMN forgejo_repo_id TO github_repo_id;
ALTER TABLE dev_box_jobs RENAME COLUMN forgejo_repo_id TO github_repo_id;

-- Dev machine links stored the provider as 'forgejo'; the product now speaks
-- GitHub (000042 did the same in the opposite direction).
ALTER TABLE dev_machines ALTER COLUMN repo_provider SET DEFAULT 'github';
UPDATE dev_machines SET repo_provider = 'github' WHERE repo_provider = 'forgejo';
UPDATE dev_machine_runtime_credentials SET credential_type = 'github_token' WHERE credential_type = 'forgejo_token';
