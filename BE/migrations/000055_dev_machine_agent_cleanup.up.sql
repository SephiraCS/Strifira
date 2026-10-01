CREATE TABLE dev_machine_agent_run_cleanup (
    agent_run_id UUID PRIMARY KEY REFERENCES dev_machine_agent_runs(id) ON DELETE CASCADE,
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_owner TEXT,
    lease_expires_at TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    CHECK ((lease_owner IS NULL) = (lease_expires_at IS NULL))
);

CREATE INDEX dev_machine_agent_run_cleanup_available_idx
    ON dev_machine_agent_run_cleanup (available_at, agent_run_id);

CREATE FUNCTION enqueue_dev_machine_agent_run_cleanup() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status IN ('succeeded', 'failed', 'cancelled', 'timeout')
       AND (TG_OP = 'INSERT' OR OLD.status NOT IN ('succeeded', 'failed', 'cancelled', 'timeout')) THEN
        INSERT INTO dev_machine_agent_run_cleanup (agent_run_id)
        VALUES (NEW.id) ON CONFLICT (agent_run_id) DO NOTHING;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER dev_machine_agent_run_cleanup_terminal
AFTER INSERT OR UPDATE OF status ON dev_machine_agent_runs
FOR EACH ROW EXECUTE FUNCTION enqueue_dev_machine_agent_run_cleanup();

-- Queue historical terminal runs once. Completed cleanup removes the queue row,
-- so reconciliation never scans all historical runs on each tick.
INSERT INTO dev_machine_agent_run_cleanup (agent_run_id)
SELECT id FROM dev_machine_agent_runs
WHERE status IN ('succeeded', 'failed', 'cancelled', 'timeout');
