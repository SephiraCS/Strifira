CREATE TABLE forgejo_user_mappings (
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    forgejo_login VARCHAR(100) NOT NULL CHECK (forgejo_login = lower(forgejo_login) AND forgejo_login <> ''),
    user_id UUID NOT NULL,
    PRIMARY KEY (workspace_id, forgejo_login),
    FOREIGN KEY (workspace_id, user_id)
        REFERENCES workspace_members(workspace_id, user_id) ON DELETE CASCADE
);
