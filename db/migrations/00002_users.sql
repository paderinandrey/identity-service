-- +goose Up
CREATE TABLE users (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email           citext NOT NULL,
    name            text NOT NULL DEFAULT '',
    active          boolean NOT NULL DEFAULT true,
    last_sign_in_at timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_email_unique UNIQUE (email)
);

COMMENT ON TABLE users IS 'Unified application users shared across GSH/DFM ecosystem';
COMMENT ON COLUMN users.id IS 'Stable application-wide user UUID; never changes';
COMMENT ON COLUMN users.email IS 'Email attribute (case-insensitive unique); not an identity key';
COMMENT ON COLUMN users.name IS 'Display name';
COMMENT ON COLUMN users.active IS 'Whether login and session validation are allowed';
COMMENT ON COLUMN users.last_sign_in_at IS 'Timestamp of the most recent successful sign-in';
COMMENT ON COLUMN users.created_at IS 'Row creation timestamp';
COMMENT ON COLUMN users.updated_at IS 'Row last update timestamp';

-- +goose Down
DROP TABLE users;
