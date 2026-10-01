ALTER TABLE forgejo_pull_requests
    ADD COLUMN body TEXT NOT NULL DEFAULT '';
