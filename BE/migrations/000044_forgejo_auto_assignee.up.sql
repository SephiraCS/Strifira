ALTER TABLE forgejo_auto_transitions
    ADD COLUMN target_assignee_id UUID,
    ADD CONSTRAINT forgejo_auto_transitions_assignee_member_fk
        FOREIGN KEY (workspace_id, target_assignee_id)
        REFERENCES workspace_members(workspace_id, user_id)
        ON DELETE SET NULL (target_assignee_id);
