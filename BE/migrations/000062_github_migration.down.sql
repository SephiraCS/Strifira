-- Reverse the GitHub migration: restore the Forgejo table (and column) names
-- and provider values. Index and constraint names are unaffected by renames
-- and keep their historical labels.
ALTER TABLE github_connections RENAME TO forgejo_connections;
ALTER TABLE github_repos RENAME TO forgejo_repos;
ALTER TABLE forgejo_repos RENAME COLUMN github_repo_id TO forgejo_repo_id;
ALTER TABLE github_pull_requests RENAME COLUMN github_repo_id TO forgejo_repo_id;
ALTER TABLE github_pull_requests RENAME TO forgejo_pull_requests;
ALTER TABLE github_pull_request_issues RENAME TO forgejo_pull_request_issues;
ALTER TABLE github_branches RENAME COLUMN github_repo_id TO forgejo_repo_id;
ALTER TABLE github_branches RENAME TO forgejo_branches;
ALTER TABLE github_commits RENAME COLUMN github_repo_id TO forgejo_repo_id;
ALTER TABLE github_commits RENAME TO forgejo_commits;
ALTER TABLE github_auto_transitions RENAME TO forgejo_auto_transitions;
ALTER TABLE forgejo_auto_transitions RENAME COLUMN assign_github_user TO assign_forgejo_user;
ALTER TABLE github_user_mappings RENAME TO forgejo_user_mappings;
ALTER TABLE forgejo_user_mappings RENAME COLUMN github_login TO forgejo_login;
ALTER TABLE github_workspace_disables RENAME TO forgejo_workspace_disables;

ALTER TABLE dev_machine_scope_settings RENAME COLUMN github_repo_id TO forgejo_repo_id;
ALTER TABLE dev_machine_checkouts RENAME COLUMN github_repo_id TO forgejo_repo_id;
ALTER TABLE dev_box_jobs RENAME COLUMN github_repo_id TO forgejo_repo_id;

ALTER TABLE dev_machines ALTER COLUMN repo_provider SET DEFAULT 'forgejo';
UPDATE dev_machines SET repo_provider = 'forgejo' WHERE repo_provider = 'github';
UPDATE dev_machine_runtime_credentials SET credential_type = 'forgejo_token' WHERE credential_type = 'github_token';
