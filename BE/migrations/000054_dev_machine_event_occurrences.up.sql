ALTER TABLE dev_machine_events
    ADD COLUMN occurrence_id UUID NOT NULL DEFAULT gen_random_uuid();

ALTER TABLE dev_machine_events
    ADD CONSTRAINT dev_machine_events_occurrence_id_key UNIQUE (occurrence_id);
