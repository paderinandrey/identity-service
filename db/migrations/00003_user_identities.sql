-- +goose Up
CREATE TABLE user_identities (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    provider   text NOT NULL,
    subject    text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT user_identities_provider_subject_unique UNIQUE (provider, subject),
    CONSTRAINT user_identities_user_provider_unique UNIQUE (user_id, provider)
);

COMMENT ON TABLE user_identities IS 'External identity mappings (SSO providers) to unified users';
COMMENT ON COLUMN user_identities.id IS 'Mapping row UUID';
COMMENT ON COLUMN user_identities.user_id IS 'Owning user; deletion restricted to preserve history';
COMMENT ON COLUMN user_identities.provider IS 'Identity provider name, e.g. okta';
COMMENT ON COLUMN user_identities.subject IS 'Stable subject issued by the provider';
COMMENT ON COLUMN user_identities.created_at IS 'When the identity was linked';

-- +goose Down
DROP TABLE user_identities;
