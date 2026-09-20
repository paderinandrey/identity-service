-- +goose Up
-- Schema only; indexes are 00010, the user_id backfill for existing rows is 00011.
ALTER TABLE user_events_outbox
    ADD COLUMN user_id        uuid,
    ADD COLUMN user_version   bigint,
    ADD COLUMN lease_until    timestamptz,
    ADD COLUMN leased_by      text,
    ADD COLUMN quarantined_at timestamptz;

COMMENT ON COLUMN user_events_outbox.user_id IS 'Subject user; events of one user publish strictly in user_version order across relay replicas';
COMMENT ON COLUMN user_events_outbox.user_version IS 'Profile version carried by the payload; the per-user publication order (created_at is transaction start time and can invert it)';
COMMENT ON COLUMN user_events_outbox.lease_until IS 'Row is being published by leased_by until this time; also the not-before time after a failed attempt';
COMMENT ON COLUMN user_events_outbox.leased_by IS 'Relay replica holding the lease';
COMMENT ON COLUMN user_events_outbox.quarantined_at IS 'Set after the retry budget is exhausted; excluded from publishing until requeued';

-- Indexes are built in 00010 (CONCURRENTLY, outside a transaction):
-- old replicas keep writing during the pre-upgrade hook.

-- +goose Down
ALTER TABLE user_events_outbox
    DROP COLUMN quarantined_at,
    DROP COLUMN leased_by,
    DROP COLUMN lease_until,
    DROP COLUMN user_version,
    DROP COLUMN user_id;
