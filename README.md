# identity-service

Identity & Access Service for the GSH / DFM ecosystem. Owns application
identities, roles, permissions, role assignments and browser sessions;
integrates with Okta (SAML) and provides authentication context to services
behind the shared GraphQL Router.

Architecture decision record: `dev/notes/architecture/gsh-dfm-shared-ui-identity.md`
(Obsidian vault, 2026-09-09). Requirements and planned work live in
[`openspec/`](openspec/) — see `AGENTS.md` for the workflow.

## Status

Implemented: SAML SSO against Okta, server-side sessions in Redis, unified
user storage in PostgreSQL, health/readiness endpoints and the internal
session-validation endpoint for the entry-point proxy.

Planned as separate OpenSpec changes: SCIM provisioning, roles/permissions,
GraphQL API, user-change events (outbox → RabbitMQ), Single Logout.

## Requirements

- [mise](https://mise.jdx.dev/) — pins Go and golangci-lint (see `mise.toml`)
- Docker (local PostgreSQL/Redis and image builds)

## Development

```bash
mise install        # install pinned Go and golangci-lint
mise run up         # start PostgreSQL (host port 5433) and Redis (host port 6380)
mise run build      # build bin/identity-service
bin/identity-service migrate                       # apply schema migrations
bin/identity-service create-user --email you@example.com --name "You"
mise run run        # run the service (serve is the default subcommand)
mise run test       # go test -race ./... (integration tests need `mise run up`)
mise run lint       # golangci-lint run
mise run vuln       # govulncheck — run for every dependency change (crewjam/saml is low-activity)
```

Non-default host ports 5433/6380 are used because 5432/6379 are commonly
taken by other local projects. Without `SAML_IDP_METADATA_URL` the service
starts with SSO routes disabled (development convenience); tests exercise
the SAML flow against an in-process mock IdP, no Okta needed.

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
- `GET /internal/session/validate` — for the entry proxy (ext-auth): 200 with
  `X-Identity-User-Id` / `X-Identity-Email` headers, 401, or 503 (fail-close).
  Must not be exposed publicly.

## Docker

```bash
docker build -t identity-service .
docker run --rm -p 8080:8080 identity-service            # serve
docker run --rm identity-service migrate                 # init container
```

The image is multi-stage: static binary (`CGO_ENABLED=0`) on
`gcr.io/distroless/static-debian12:nonroot`. The process handles SIGTERM
itself (it runs as PID 1) and shuts down gracefully.
