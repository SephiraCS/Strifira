-- Optional per-workspace organization restriction: when set, repo discovery
-- and linking only accept repositories owned by this organization.
ALTER TABLE forgejo_connections
    ADD COLUMN organization VARCHAR(255) NULL;
