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
   SET user_id      = (payload -> 'user' ->> 'id')::uuid,
       user_version = (payload -> 'user' ->> 'version')::bigint
 WHERE user_id IS NULL
   AND published_at IS NULL
   AND payload -> 'user' ->> 'id' IS NOT NULL;

-- +goose Down
-- Deliberately a no-op: the values still live in the payload, and 00009's
-- Down drops both columns anyway. Rewriting every retained row here would
-- lock and WAL the whole history for nothing (Codex review, PR #6).
SELECT 1;
