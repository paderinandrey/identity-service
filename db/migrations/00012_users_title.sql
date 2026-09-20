-- +goose Up
-- ADD COLUMN with a constant default does not rewrite the table; replicas of
-- the previous release ignore the column, so this is rolling-safe.
ALTER TABLE users ADD COLUMN title text NOT NULL DEFAULT '';
COMMENT ON COLUMN users.title IS 'Job title from provisioning (SCIM title); empty when unknown';

-- +goose Down
ALTER TABLE users DROP COLUMN title;
