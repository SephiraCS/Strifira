ALTER TABLE forgejo_auto_transitions
    ADD COLUMN assign_forgejo_user BOOLEAN NOT NULL DEFAULT FALSE,
    ADD CONSTRAINT forgejo_auto_transitions_assignee_mode_ck
        CHECK (NOT (assign_forgejo_user AND target_assignee_id IS NOT NULL));

UPDATE forgejo_auto_transitions
SET assign_forgejo_user = TRUE
WHERE target_status = 'in_progress' AND target_assignee_id IS NULL;
