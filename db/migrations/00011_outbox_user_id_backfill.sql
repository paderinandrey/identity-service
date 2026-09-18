-- +goose Up
-- Data migration, no schema change: rows recorded before 00009 carry the
-- user only inside the payload. Only pending rows are backfilled — that
-- set is small by construction (it is what the relay is about to
-- publish), so the statement is bounded and restartable. Published
-- history is left as is: it is never claimed, the relay does not need
-- its user_id, and retention removes it. Rewriting all of history in one
-- pre-upgrade transaction would hold locks and generate WAL for no
-- benefit (Codex review, PR #6).
UPDATE user_events_outbox
   SET user_id = (payload -> 'user' ->> 'id')::uuid
 WHERE user_id IS NULL
   AND published_at IS NULL
   AND payload -> 'user' ->> 'id' IS NOT NULL;

-- +goose Down
-- Restores the pre-backfill state exactly: the value lives in the payload.
UPDATE user_events_outbox SET user_id = NULL;
