-- Update already-deployed databases as well as fresh installations. Valid
-- maximum results can exceed 2 MiB once patch and metadata strings are escaped.
ALTER TABLE dev_box_jobs
    DROP CONSTRAINT dev_box_jobs_result_check,
    ADD CONSTRAINT dev_box_jobs_result_check
        CHECK (result IS NULL OR (jsonb_typeof(result) = 'object' AND octet_length(result::text) <= 25165824));
