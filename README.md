# identity-service

Identity & Access Service for the GSH / DFM ecosystem. Owns application identities, roles, permissions, role assignments and browser sessions; integrates with Okta (SAML sign-in, SCIM provisioning) and provides authentication context to services behind the shared GraphQL Router. It does not own sourcing or DFM workflows, and GSH and DFM keep their own domain authorization checks.

| | |
| --- | --- |
| Owner | Andrey Paderin |
| Status | what is implemented and what is not deployed yet: [Architecture](docs/ARCHITECTURE.md#scope) |
| Runtime | Go, net/http, gqlgen (Federation v2), crewjam/saml, PostgreSQL (pgx + goose), Redis sessions, RabbitMQ |
| Specs | [`openspec/`](openspec/) — workflow in `AGENTS.md` |

## Quick start

```bash
mise install
mise run up                          # PostgreSQL, Redis, RabbitMQ
mise run build && bin/identity-service migrate
mise run run                         # serve on :8080 (public) and :8081 (internal)
curl -s localhost:8080/readyz
```

Signing in locally needs Keycloak: [Local SSO with Keycloak](docs/DEVELOPMENT.md#local-sso-with-keycloak).

## Documentation

| Doc | Read it to |
| --- | --- |
| [Getting started](docs/GETTING_STARTED.md) | go from a fresh clone to a running service |
| [Development](docs/DEVELOPMENT.md) | run, test, sign in locally with Keycloak, regenerate docs |
| [Configuration](docs/CONFIGURATION.md) | look up an environment variable (generated) |
| [Architecture](docs/ARCHITECTURE.md) | see the scope, components, access model, sessions, relay, observability |
| [Vocabulary](docs/VOCABULARY.md) | use the same words as the code and the specs |
| [API](docs/API.md) | look up the routes on both listeners and the GraphQL rules |
| [Integrations](docs/INTEGRATIONS.md) | wire Okta, the entry proxy, the GraphQL Router or an event consumer |
| [User events](docs/events/user-events.md) | consume user events |
| [Deployment](docs/DEPLOYMENT.md) | build the image, install the chart |
| [Local stand](docs/playbooks/LOCAL_STAND.md) | run and verify the whole contour locally |
| [Access setup](docs/playbooks/ACCESS_SETUP.md) | bootstrap applications and roles, import assignments |
| [Frontend e2e](docs/playbooks/FRONTEND_E2E.md) | sign test users in without the IdP form |
| [Index](docs/INDEX.md) | find every document, generated ones and OpenSpec included |
