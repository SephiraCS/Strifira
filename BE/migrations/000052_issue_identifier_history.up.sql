-- Full identifiers remain attached to their original issue after a team rename.
-- Only identities known at migration time are seeded; earlier renames require
-- an explicit, verified restoration rather than inference from issue numbers.
CREATE TABLE issue_identifier_history (
    workspace_id UUID NOT NULL,
    identifier_text VARCHAR(50) NOT NULL,
    issue_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (workspace_id, identifier_text),
    FOREIGN KEY (workspace_id, issue_id)
        REFERENCES issues(workspace_id, id) ON DELETE CASCADE
);

CREATE INDEX issue_identifier_history_issue
    ON issue_identifier_history (workspace_id, issue_id);

INSERT INTO issue_identifier_history (workspace_id, identifier_text, issue_id)
SELECT workspace_id, identifier_text, id FROM issues;

CREATE FUNCTION record_issue_identifier()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        INSERT INTO issue_identifier_history (workspace_id, identifier_text, issue_id)
        VALUES (OLD.workspace_id, OLD.identifier_text, OLD.id)
        ON CONFLICT (workspace_id, identifier_text) DO UPDATE
            SET issue_id = EXCLUDED.issue_id
            WHERE issue_identifier_history.issue_id = EXCLUDED.issue_id;

        IF NOT FOUND THEN
            RAISE EXCEPTION 'issue identifier already belongs to another issue'
                USING ERRCODE = '23505', CONSTRAINT = 'issue_identifier_history_pkey';
        END IF;
    END IF;

    INSERT INTO issue_identifier_history (workspace_id, identifier_text, issue_id)
    VALUES (NEW.workspace_id, NEW.identifier_text, NEW.id)
    ON CONFLICT (workspace_id, identifier_text) DO UPDATE
        SET issue_id = EXCLUDED.issue_id
        WHERE issue_identifier_history.issue_id = EXCLUDED.issue_id;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'issue identifier already belongs to another issue'
            USING ERRCODE = '23505', CONSTRAINT = 'issue_identifier_history_pkey';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER issue_record_identifier
    AFTER INSERT OR UPDATE OF identifier_text ON issues
    FOR EACH ROW EXECUTE FUNCTION record_issue_identifier();
