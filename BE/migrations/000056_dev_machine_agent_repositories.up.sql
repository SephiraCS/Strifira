-- Repository selection is immutable per run. Machine branches and workspace
-- links may change while a run is executing or awaiting terminal cleanup.
CREATE TABLE dev_machine_agent_run_repositories (
    agent_run_id UUID PRIMARY KEY REFERENCES dev_machine_agent_runs(id) ON DELETE CASCADE,
    machine_id UUID NOT NULL REFERENCES dev_machines(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    selection_id UUID NOT NULL,
    repository_full_name TEXT NOT NULL,
    repository_url TEXT NOT NULL,
    base_branch TEXT NOT NULL,
    working_branch TEXT NOT NULL,
    workspace_path TEXT NOT NULL
);
