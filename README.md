# identity-service

Identity & Access Service for the GSH / DFM ecosystem. Owns application
identities, roles, permissions, role assignments and browser sessions;
integrates with Okta (SAML) and provides authentication context to services
behind the shared GraphQL Router.

Architecture decision record: `dev/notes/architecture/gsh-dfm-shared-ui-identity.md`
(Obsidian vault, 2026-09-09). Requirements and planned work live in
[`openspec/`](openspec/) — see `AGENTS.md` for the workflow.

## Status

Skeleton stage: HTTP server with health endpoints, configuration and logging.
SSO, sessions, storage and GraphQL are planned as separate OpenSpec changes.
The docker-compose PostgreSQL and Redis containers are infrastructure for
upcoming changes; the application does not connect to them yet.

## Requirements

- [mise](https://mise.jdx.dev/) — pins Go and golangci-lint (see `mise.toml`)
- Docker (for local infrastructure and image builds)

## Development

```bash
mise install        # install pinned Go and golangci-lint
mise run build      # build bin/identity-service
mise run test       # go test -race ./...
mise run lint       # golangci-lint run
mise run run        # run the service locally
mise run up         # start PostgreSQL (host port 5433) and Redis (host port 6380)
```

Non-default host ports 5433/6380 are used because 5432/6379 are commonly
taken by other local projects.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `APP_ENV` | `development` | One of `development`, `staging`, `production` |
| `LOG_LEVEL` | `info` | One of `debug`, `info`, `warn`, `error` |
| `SHUTDOWN_TIMEOUT` | `10s` | Grace period for in-flight requests on shutdown |

Invalid values fail startup with a non-zero exit code.

## Endpoints

- `GET /healthz` — liveness: 200 while the process serves HTTP
- `GET /readyz` — readiness: 200 when accepting traffic, 503 during shutdown

## Docker

```bash
docker build -t identity-service .
docker run --rm -p 8080:8080 identity-service
```

The image is multi-stage: static binary (`CGO_ENABLED=0`) on
`gcr.io/distroless/static-debian12:nonroot`. The process handles SIGTERM
itself (it runs as PID 1) and shuts down gracefully.
