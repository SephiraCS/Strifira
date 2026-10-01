-- One ALTER statement preserves the current constraint and every result if
-- existing larger results prevent reinstating the earlier bound.
ALTER TABLE dev_box_jobs
    DROP CONSTRAINT dev_box_jobs_result_check,
    ADD CONSTRAINT dev_box_jobs_result_check
        CHECK (result IS NULL OR (jsonb_typeof(result) = 'object' AND octet_length(result::text) <= 2097152));
