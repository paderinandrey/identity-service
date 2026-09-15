-- +goose Up
-- Schema only; the user_id backfill for existing rows is 00010.
ALTER TABLE user_events_outbox
    ADD COLUMN user_id        uuid,
    ADD COLUMN lease_until    timestamptz,
    ADD COLUMN leased_by      text,
    ADD COLUMN quarantined_at timestamptz;

COMMENT ON COLUMN user_events_outbox.user_id IS 'Subject user; events of one user publish strictly in (created_at, id) order across relay replicas';
COMMENT ON COLUMN user_events_outbox.lease_until IS 'Row is being published by leased_by until this time; also the not-before time after a failed attempt';
COMMENT ON COLUMN user_events_outbox.leased_by IS 'Relay replica holding the lease';
COMMENT ON COLUMN user_events_outbox.quarantined_at IS 'Set after the retry budget is exhausted; excluded from publishing until requeued';

DROP INDEX user_events_outbox_unpublished_idx;
-- Eligible rows in publication order.
CREATE INDEX user_events_outbox_pending_idx
    ON user_events_outbox (created_at, id) WHERE published_at IS NULL AND quarantined_at IS NULL;
-- Head-of-line check per user.
CREATE INDEX user_events_outbox_user_pending_idx
    ON user_events_outbox (user_id, created_at, id) WHERE published_at IS NULL AND quarantined_at IS NULL;
-- Retention of published rows.
CREATE INDEX user_events_outbox_published_idx
    ON user_events_outbox (published_at) WHERE published_at IS NOT NULL;

-- +goose Down
DROP INDEX user_events_outbox_published_idx;
DROP INDEX user_events_outbox_user_pending_idx;
DROP INDEX user_events_outbox_pending_idx;
CREATE INDEX user_events_outbox_unpublished_idx
    ON user_events_outbox (created_at) WHERE published_at IS NULL;
ALTER TABLE user_events_outbox
    DROP COLUMN quarantined_at,
    DROP COLUMN leased_by,
    DROP COLUMN lease_until,
    DROP COLUMN user_id;
