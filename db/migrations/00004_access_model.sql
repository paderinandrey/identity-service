-- +goose Up
CREATE TABLE applications (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT applications_name_unique UNIQUE (name)
);

COMMENT ON TABLE applications IS 'Business applications owning roles and permissions (e.g. gsh, dfm)';
COMMENT ON COLUMN applications.id IS 'Application UUID';
COMMENT ON COLUMN applications.name IS 'Unique application name';
COMMENT ON COLUMN applications.created_at IS 'Row creation timestamp';

CREATE TABLE roles (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    application_id uuid NOT NULL REFERENCES applications (id) ON DELETE RESTRICT,
    name           text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT roles_application_name_unique UNIQUE (application_id, name),
    -- Composite key target letting role_permissions enforce same-application invariant.
    CONSTRAINT roles_id_application_unique UNIQUE (id, application_id)
);

COMMENT ON TABLE roles IS 'Roles owned by applications; assigned to users via user_roles';
COMMENT ON COLUMN roles.id IS 'Role UUID';
COMMENT ON COLUMN roles.application_id IS 'Owning application';
COMMENT ON COLUMN roles.name IS 'Role name, unique within the application';
COMMENT ON COLUMN roles.created_at IS 'Row creation timestamp';

CREATE TABLE permissions (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    application_id uuid NOT NULL REFERENCES applications (id) ON DELETE RESTRICT,
    name           text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT permissions_application_name_unique UNIQUE (application_id, name),
    CONSTRAINT permissions_id_application_unique UNIQUE (id, application_id)
);

COMMENT ON TABLE permissions IS 'Permissions owned by applications; granted to users through roles';
COMMENT ON COLUMN permissions.id IS 'Permission UUID';
COMMENT ON COLUMN permissions.application_id IS 'Owning application';
COMMENT ON COLUMN permissions.name IS 'Permission name, unique within the application';
COMMENT ON COLUMN permissions.created_at IS 'Row creation timestamp';

CREATE TABLE role_permissions (
    role_id        uuid NOT NULL,
    permission_id  uuid NOT NULL,
    -- Duplicated owning application: composite FKs below guarantee the role
    -- and the permission belong to the same application.
    application_id uuid NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (role_id, permission_id),
    FOREIGN KEY (role_id, application_id) REFERENCES roles (id, application_id) ON DELETE CASCADE,
    FOREIGN KEY (permission_id, application_id) REFERENCES permissions (id, application_id) ON DELETE CASCADE
);

COMMENT ON TABLE role_permissions IS 'Role composition; same-application invariant enforced by composite FKs';
COMMENT ON COLUMN role_permissions.role_id IS 'Role';
COMMENT ON COLUMN role_permissions.permission_id IS 'Permission included in the role';
COMMENT ON COLUMN role_permissions.application_id IS 'Application owning both the role and the permission';
COMMENT ON COLUMN role_permissions.created_at IS 'When the permission was added to the role';

-- +goose Down
DROP TABLE role_permissions;
DROP TABLE permissions;
DROP TABLE roles;
DROP TABLE applications;
