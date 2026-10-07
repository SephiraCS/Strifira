-- Backfill a "created" activity event for issues created before creation
-- started being recorded, so the activity feed shows who created them.
INSERT INTO issue_history (id, issue_id, user_id, field, old_value, new_value, created_at)
SELECT gen_random_uuid(), i.id, i.creator_id, 'created', NULL, NULL, i.created_at
FROM issues i
WHERE NOT EXISTS (
    SELECT 1 FROM issue_history h
    WHERE h.issue_id = i.id AND h.field = 'created'
);
