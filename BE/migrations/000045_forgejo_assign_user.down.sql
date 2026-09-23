ALTER TABLE forgejo_auto_transitions
    DROP CONSTRAINT forgejo_auto_transitions_assignee_mode_ck,
    DROP COLUMN assign_forgejo_user;
