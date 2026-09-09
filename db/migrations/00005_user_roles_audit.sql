-- +goose Up
CREATE TABLE user_roles (
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    role_id    uuid NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    granted_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, role_id)
);

COMMENT ON TABLE user_roles IS 'Role assignments; the only source of truth for user access';
COMMENT ON COLUMN user_roles.user_id IS 'Assigned user';
COMMENT ON COLUMN user_roles.role_id IS 'Assigned role';
COMMENT ON COLUMN user_roles.granted_by IS 'Actor that granted the role (cli or a user reference)';
COMMENT ON COLUMN user_roles.created_at IS 'When the role was granted';

CREATE TABLE access_audit_log (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    actor          text NOT NULL,
    action         text NOT NULL,
    target_user_id uuid REFERENCES users (id) ON DELETE RESTRICT,
    -- No FK on purpose: the journal must outlive deleted roles.
    role_id        uuid,
    details        jsonb NOT NULL DEFAULT '{}',
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX access_audit_log_target_user_idx ON access_audit_log (target_user_id);
CREATE INDEX access_audit_log_created_at_idx ON access_audit_log (created_at);

COMMENT ON TABLE access_audit_log IS 'Append-only journal of access changes, written in the same transaction as the change';
COMMENT ON COLUMN access_audit_log.id IS 'Journal entry UUID';
COMMENT ON COLUMN access_audit_log.actor IS 'Who performed the change (cli or a user reference)';
COMMENT ON COLUMN access_audit_log.action IS 'Action name, e.g. role.grant, role.revoke, role.composition';
COMMENT ON COLUMN access_audit_log.target_user_id IS 'Affected user, when the action targets a user';
COMMENT ON COLUMN access_audit_log.role_id IS 'Affected role; kept without FK so history survives role deletion';
COMMENT ON COLUMN access_audit_log.details IS 'Action details (application/role/permission names)';
COMMENT ON COLUMN access_audit_log.created_at IS 'When the change happened';

-- +goose Down
DROP TABLE access_audit_log;
DROP TABLE user_roles;
