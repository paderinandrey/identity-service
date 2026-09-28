# API

## Public listener (`LISTEN_ADDR`)

- `GET /healthz` — liveness: 200 while the process serves HTTP
- `GET /readyz` — readiness: 200 when accepting traffic and PostgreSQL/Redis respond; 503 during shutdown or dependency outage
- `GET /auth/saml/init?next=/path` — start SSO, redirects to Okta
- `POST /auth/saml/acs` — SAML assertion consumer (Okta posts here)
- `GET /auth/saml/metadata` — SP metadata XML for the IdP configuration
- `GET /auth/me` — current user (id, email, name) or 401
- `POST /auth/logout` — destroy session (Origin-checked)
- `POST /graphql` — GraphQL subgraph (session-authenticated), see [GraphQL](#graphql)
- `/scim/v2/*` — SCIM 2.0 provisioning for Okta, mounted only with `SCIM_TOKEN`; see [Integrations](INTEGRATIONS.md#okta-scim-provisioning)

## Internal listener (`INTERNAL_LISTEN_ADDR`)

- `GET /internal/session/validate` — for the entry proxy (ext-auth): 200 with `X-Identity-User-Id`, `X-Identity-Email` and `X-Identity-Permissions` (comma-separated `app:permission`, empty when the user has no roles) headers, 401, or 503 (fail-close). The permissions header is an interim contract until a signed internal token is chosen.
- `GET /internal/metrics` — Prometheus metrics; see [Observability](ARCHITECTURE.md#observability)
- `POST /internal/e2e/login` — programmatic test sessions, only with `E2E_LOGIN_TOKEN`; see [the frontend e2e playbook](playbooks/FRONTEND_E2E.md)

Everything under `/internal/` — validation, `/internal/metrics`, the e2e login — is served **only on the internal listener** (`INTERNAL_LISTEN_ADDR`); on the public port those paths are a 404 by construction, not by routing. The proxy overwrites any client-supplied `X-Identity-*` header with the validation response (an empty permissions header included), so upstreams never see a forged context; the chart's NetworkPolicy keeps the internal port reachable only from the proxy, monitoring and explicitly listed peers.

## GraphQL

`POST /graphql` is an Apollo Federation v2 subgraph (`User` is an entity keyed by `id`); authentication is the browser session cookie. Directory queries (`me`, `users`, `user`, `applications`) are open to any authenticated user; `grantRole` / `revokeRole` mutations and `accessAuditLog` require the `identity:access.manage` permission and record the caller as the audit actor. The schema is `internal/graphql/schema.graphqls`.

```graphql
query { me { user { id name } permissions } }
mutation { grantRole(userId: "…", role: "gsh/sourcing_manager") { roles { role } } }
```

Limits on `POST /graphql`, all enforced before any resolver runs: the request body is capped at 1 MiB (like SAML and SCIM), and every query is priced against a complexity budget of 2000 that multiplies list fields by the requested size (`users` by `first`, `accessAuditLog` by `limit`, `applications` by a fixed directory weight), so aliasing a 200-user page ten times is rejected up front. `users` is a keyset-paged connection — `users(first: 50, after: <endCursor>) { nodes { … } pageInfo { endCursor hasNextPage } }` — with `first` capped at 200; a page's role assignments are loaded in one query. Federation `_entities` batches are capped at 200 representations per operation, checked before execution. The HTTP server has 30 s read/write timeouts and a 120 s idle timeout.

Cookie-authenticated **mutations** are checked for a trusted `Origin` (base and frontend URLs, the same rule as logout); a request without an Origin — the router, a CLI — passes, a foreign origin gets `FORBIDDEN` before any resolver runs. Reads are not checked. How the router must be configured for this: [Integrations](INTEGRATIONS.md#graphql-router-and-other-subgraphs).

## Events

User-change events on RabbitMQ are a public contract of their own: [`docs/events/user-events.md`](events/user-events.md).
