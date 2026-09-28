# Playbook: set up access

Use it to bootstrap applications and roles in a new environment, to appoint the first access administrator, or to load many assignments at once during a cutover. Day-to-day grants go through the GraphQL `grantRole` / `revokeRole` mutations ([API](../API.md#graphql)).

## Declare applications and roles

Bootstrap is declarative — `seed-access` reconciles this shape (creating what is missing and aligning role composition, never touching assignments):

```yaml
applications:
  - name: identity
    permissions: [access.manage]
    roles:
      - name: admin
        permissions: [access.manage]
  - name: gsh
    permissions: [orders.read, orders.write]
    roles:
      - name: sourcing_manager
        permissions: [orders.read, orders.write]
```

```bash
bin/identity-service seed-access --file access.yaml
```

## Appoint the first administrator

Grant the first access administrator once via CLI:

```bash
bin/identity-service grant-role --email admin@example.com --role identity/admin
```

## Import assignments in bulk

To load many assignments at once — the cutover from a system that kept its own roles, such as GSH's `users_roles` — use `import-assignments`:

```yaml
assignments:
  - email: ada@example.com          # an active user, matched by email
    roles: [gsh/observer, gsh/operator]
  - id: 0f2b7c1a-3d4e-4f5a-8b6c-7d8e9f0a1b2c   # or by global id
    roles: [dfm/engineer]
```

```bash
bin/identity-service import-assignments --file assignments.yaml --dry-run   # report only
bin/identity-service import-assignments --file assignments.yaml
```

The file is validated as a whole before anything is written. Each entry is one transaction: an unknown user or role fails that entry and rolls it back, the other entries still apply, and the command exits non-zero so the failure is not missed. Roles already held are left alone and not journaled, so rerunning the same file after fixing it is safe. The import only adds; it never revokes roles missing from the file.

Cutover order: provision users over SCIM, `replay-users` for the projections, then `import-assignments`.

## Checks

Every change appears in `access_audit_log`; the GraphQL `accessAuditLog` query shows it to an access administrator.
