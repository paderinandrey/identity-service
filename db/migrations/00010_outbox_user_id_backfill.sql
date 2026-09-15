-- +goose Up
-- Data migration, no schema change: rows recorded before 00009 carry the
-- user only inside the payload. The table is small (user-change events),
-- so one statement is within bounds; a larger table would need batches.
UPDATE user_events_outbox
   SET user_id = (payload -> 'user' ->> 'id')::uuid
 WHERE user_id IS NULL
   AND payload -> 'user' ->> 'id' IS NOT NULL;

-- +goose Down
-- Restores the pre-backfill state exactly: the value lives in the payload.
UPDATE user_events_outbox SET user_id = NULL;
