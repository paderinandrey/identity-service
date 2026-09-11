# identity-service

Identity & Access Service for the GSH / DFM ecosystem. Owns application
identities, roles, permissions, role assignments and browser sessions;
integrates with Okta (SAML) and provides authentication context to services
behind the shared GraphQL Router.

Architecture decision record: `dev/notes/architecture/gsh-dfm-shared-ui-identity.md`
(Obsidian vault, 2026-09-09). Requirements and planned work live in
[`openspec/`](openspec/) — see `AGENTS.md` for the workflow. CI (GitHub
Actions) runs lint, race tests against PostgreSQL/Redis/RabbitMQ services,
govulncheck and a gqlgen drift check on every push and pull request.

## Status

Implemented: SAML SSO against Okta, server-side sessions in Redis, unified
user storage in PostgreSQL, access control (applications, roles, permissions,
assignments, append-only audit journal), a GraphQL federation subgraph for
profiles and access management, health/readiness endpoints and the internal
session-validation endpoint for the entry-point proxy.

Also implemented: SCIM 2.0 provisioning from Okta (create/update/deactivate
with immediate session revocation), user-change events delivered to RabbitMQ
through a transactional outbox, and observability (Sentry error reporting,
Prometheus metrics, panic recovery).

Planned as separate OpenSpec changes: Single Logout, GraphQL Router
integration.

## Requirements

- [mise](https://mise.jdx.dev/) — pins Go and golangci-lint (see `mise.toml`)
- Docker (local PostgreSQL/Redis and image builds)

## Development

```bash
mise install        # install pinned Go and golangci-lint
mise run up         # PostgreSQL (5433), Redis (6380), RabbitMQ (5673, UI on 15673)
mise run build      # build bin/identity-service
bin/identity-service migrate                       # apply schema migrations
bin/identity-service create-user --email you@example.com --name "You"
bin/identity-service seed-access --file access.yaml     # applications/roles/permissions
bin/identity-service grant-role --email you@example.com --role gsh/sourcing_manager
bin/identity-service revoke-role --email you@example.com --role gsh/sourcing_manager
bin/identity-service replay-users                       # enqueue user snapshots for consumers
mise run run        # run the service (serve is the default subcommand)
mise run test       # go test -race ./... (integration tests need `mise run up`)
mise run lint       # golangci-lint run
mise run vuln       # govulncheck — run for every dependency change (crewjam/saml is low-activity)
mise run generate   # regenerate gqlgen code after editing internal/graphql/schema.graphqls
```

Non-default host ports 5433/6380 are used because 5432/6379 are commonly
taken by other local projects. Without `SAML_IDP_METADATA_URL` the service
starts with SSO routes disabled (development convenience); tests exercise
the SAML flow against an in-process mock IdP, no Okta needed.

### Local SSO with Keycloak

To actually sign in locally, start Keycloak (SAML IdP) with a preconfigured
realm and point the service at it:

```bash
mise run up-sso     # infrastructure + Keycloak on http://localhost:8081 (admin/admin)
bin/identity-service create-user --email qa@example.com --name "QA User"
SAML_IDP_METADATA_URL="http://localhost:8081/realms/identity/protocol/saml/descriptor" mise run run
open http://localhost:8080/auth/saml/init   # sign in as qa@example.com / password
```

The imported realm (`dev/keycloak/realm-identity.json`) contains a SAML
client for this service (signed responses and assertions, NameID = email)
and the test user. Note: Keycloak issues Secure cookies even over http —
browsers accept them on localhost (secure context), non-browser HTTP
clients need to opt in.

## Configuration

| Variable | Default (development) | Description |
| --- | --- | --- |
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `APP_ENV` | `development` | One of `development`, `staging`, `production` |
| `LOG_LEVEL` | `info` | One of `debug`, `info`, `warn`, `error` |
| `SHUTDOWN_TIMEOUT` | `10s` | Grace period for in-flight requests on shutdown |
| `DATABASE_URL` | compose DB on `localhost:5433` | PostgreSQL connection string |
| `REDIS_URL` | `redis://localhost:6380/0` | Redis connection string (sessions) |
| `BASE_URL` | `http://localhost:8080` | Public base URL of this service |
| `FRONTEND_BASE_URL` | `http://localhost:8080` | Where the browser lands after login |
| `SAML_IDP_METADATA_URL` | — (SSO disabled) | Okta IdP metadata URL |
| `RELAY_STATE_SECRET` | insecure dev value | HMAC secret for the RelayState token |
| `SESSION_COOKIE_NAME` | `__identity_session` | Session cookie name |
| `SESSION_IDLE_TIMEOUT` | `24h` | Session idle expiry (slides with activity) |
| `SESSION_LIFETIME` | `720h` | Absolute session lifetime |
| `SESSIONS_MAX_CONCURRENT` | `100` | Per-user session cap; oldest are evicted |
| `USER_REVOCATION_DELAY` | `60s` | Max staleness of the user active-flag cache |
| `PERMISSIONS_CACHE_TTL` | `60s` | Max staleness of effective permissions (revocation delay) |
| `SCIM_TOKEN` | — (SCIM disabled) | Bearer token for the Okta SCIM client (min 32 chars) |
| `RABBITMQ_URL` | compose broker on `localhost:5673` | RabbitMQ connection string (user events) |
| `EVENTS_EXCHANGE` | `identity.events` | Topic exchange for user-change events |
| `SENTRY_DSN` | — (Sentry disabled) | Error-reporting DSN; panics and error-level logs become issues |

Outside development the connection strings, URLs and secrets are required;
missing ones fail startup with an explicit list. Invalid values fail startup
with a non-zero exit code.

## Endpoints

- `GET /healthz` — liveness: 200 while the process serves HTTP
- `GET /readyz` — readiness: 200 when accepting traffic and PostgreSQL/Redis
  respond; 503 during shutdown or dependency outage
- `GET /auth/saml/init?next=/path` — start SSO, redirects to Okta
- `POST /auth/saml/acs` — SAML assertion consumer (Okta posts here)
- `GET /auth/saml/metadata` — SP metadata XML for the IdP configuration
- `GET /auth/me` — current user (id, email, name) or 401
- `POST /auth/logout` — destroy session (Origin-checked)
- `POST /graphql` — GraphQL subgraph (session-authenticated), see below
- `GET /internal/session/validate` — for the entry proxy (ext-auth): 200 with
  `X-Identity-User-Id`, `X-Identity-Email` and `X-Identity-Permissions`
  (comma-separated `app:permission`, empty when the user has no roles) headers,
  401, or 503 (fail-close). Must not be exposed publicly. The permissions
  header is an interim contract until a signed internal token is chosen.

## Access control

Roles and permissions belong to applications; assignments are the only
source of truth for user access and change only in this service. Every
change is journaled in `access_audit_log` within the same transaction.
Bootstrap is declarative — `seed-access` reconciles this shape (creating
what is missing and aligning role composition, never touching assignments):

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

Grant the first access administrator once via CLI:
`bin/identity-service grant-role --email admin@example.com --role identity/admin`.

## GraphQL

`POST /graphql` is an Apollo Federation v2 subgraph (`User` is an entity
keyed by `id`); authentication is the browser session cookie. Directory
queries (`me`, `users`, `user`, `applications`) are open to any
authenticated user; `grantRole` / `revokeRole` mutations and
`accessAuditLog` require the `identity:access.manage` permission and
record the caller as the audit actor.

```graphql
query { me { user { id name } permissions } }
mutation { grantRole(userId: "…", role: "gsh/sourcing_manager") { roles { role } } }
```

## SCIM provisioning

With `SCIM_TOKEN` set, `/scim/v2/*` serves the SCIM 2.0 subset used by
Okta: `Users` CRUD with PATCH, `userName eq` / `externalId eq` filters,
pagination and discovery (`ServiceProviderConfig`, `ResourceTypes`,
`Schemas`). Authentication is the static bearer token; without the token
the routes are not mounted at all.

- `userName` maps to email, `displayName` to name; `externalId` (Okta's
  stable user id) is stored as an `okta-scim` external identity.
- Deactivation (`active=false` via PUT/PATCH, or DELETE) is soft: the user
  keeps their UUID, history and role assignments, and **all their sessions
  are destroyed immediately** — deactivation in Okta locks the person out
  at once. Reactivation is supported; old sessions do not come back.
- Groups are not supported by design: role assignments live in this
  service only (see Access control above).

Okta app setup: SCIM connector base URL `BASE_URL/scim/v2`, auth mode
"HTTP Header" with the bearer token.

## User events

Every user change (create, profile update, deactivate/reactivate) writes an
event into the `user_events_outbox` table **in the same transaction**; a
relay inside `serve` publishes them to the durable topic exchange
`identity.events` with publisher confirms. Routing keys: `user.created`,
`user.updated`, `user.deactivated`, `user.reactivated`, `user.snapshot`.

Message body (`schemaVersion` 1):

```json
{"id": "…", "type": "identity.user.updated", "schemaVersion": 1,
 "occurredAt": "2026-09-10T00:00:00Z",
 "user": {"id": "…", "email": "…", "name": "…", "active": true, "version": 4}}
```

`user.version` grows with every change — consumers must drop updates with a
version not greater than the one already applied, and deduplicate by the
AMQP `message_id` (equal to `id`). Delivery is at-least-once; events survive
broker outages and service restarts in the outbox. `replay-users` enqueues
`user.snapshot` events for every user to bootstrap or repair a projection.
The broker is deliberately excluded from `/readyz`.

## Observability

- Logs are JSONL on stdout (`log/slog`). With `SENTRY_DSN` set, error-level
  records and handler panics are additionally reported to Sentry as issues
  (environment = `APP_ENV`); panics always return 500 and never kill the
  process.
- `GET /internal/metrics` serves Prometheus metrics (internal zone, same as
  the validate endpoint): standard Go/process collectors,
  `http_request_duration_seconds` / `http_requests_total` by route pattern,
  `outbox_pending` / `events_published_total` / `event_publish_errors_total`,
  `sign_ins_total{result}`, `scim_operations_total{op}` and
  `cache_requests_total{cache,result}` for the permissions/active caches.

## Docker

```bash
docker build -t identity-service .
docker run --rm -p 8080:8080 identity-service            # serve
docker run --rm identity-service migrate                 # init container
```

The image is multi-stage: static binary (`CGO_ENABLED=0`) on
`gcr.io/distroless/static-debian12:nonroot`. The process handles SIGTERM
itself (it runs as PID 1) and shuts down gracefully.
