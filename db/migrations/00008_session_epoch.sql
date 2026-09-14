-- +goose Up
-- Additive, rolling-safe: older replicas ignore the column.
ALTER TABLE users ADD COLUMN session_epoch bigint NOT NULL DEFAULT 0;
COMMENT ON COLUMN users.session_epoch IS 'Session generation: bumped in the deactivation transaction; a session is valid only while the epoch it recorded at sign-in equals this value';

-- +goose Down
ALTER TABLE users DROP COLUMN session_epoch;
