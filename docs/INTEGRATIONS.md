# Integrations

| Neighbour | Direction | Protocol | Contract |
| --- | --- | --- | --- |
| Okta | Okta → service | SAML 2.0 (sign-in), SCIM 2.0 (provisioning) | [Okta SAML](#okta-saml-sign-in), [Okta SCIM](#okta-scim-provisioning) |
| Entry proxy (Envoy Gateway) | proxy → service | HTTP ext-auth | [Entry proxy](#entry-proxy-envoy-ext-auth) |
| GraphQL Router (Cosmo) | router → service | GraphQL Federation v2 | [GraphQL Router](#graphql-router-and-other-subgraphs) |
| GSH, DFM, other consumers | service → consumers | RabbitMQ topic exchange | [User events](#user-events-rabbitmq) |

## Okta SAML sign-in

The identity key is Okta's immutable user id, carried by both channels:

- **SAML**: Name ID format `Persistent`, Application username = custom expression `user.id`. Attribute statements: `email` → `user.email`, optionally `name` → `user.displayName`. The service compares the `email` attribute with the directory and logs a drift, but SCIM stays the only writer of the address.
- **SCIM**: connector base URL `BASE_URL/scim/v2`, auth mode "HTTP Header" with the bearer token. Check the profile mapping: `externalId` must map to `user.id` (Okta's default), so that it equals the SAML NameID.
- Sign-in is SP-initiated: the service records every AuthnRequest id and accepts each assertion once (`InResponseTo` and assertion ids are one-shot in Redis, shared by all replicas). IdP-initiated sign-in from the Okta dashboard needs `SAML_ALLOW_IDP_INITIATED=true`.

## Okta SCIM provisioning

With `SCIM_TOKEN` set, `/scim/v2/*` serves the SCIM 2.0 subset used by Okta: `Users` CRUD with PATCH, `userName eq` / `externalId eq` filters, pagination and discovery (`ServiceProviderConfig`, `ResourceTypes`, `Schemas`). Authentication is the static bearer token; without the token the routes are not mounted at all.

- `userName` maps to email, `displayName` to name, `title` to the job title (optional; map Okta's `title` attribute in the SCIM application or it stays empty); `externalId` (Okta's immutable user id) is stored as the `okta` external identity — the same subject SAML sign-in resolves by. Email is a mutable attribute owned by SCIM; a SAML assertion never writes it.
- Every request is one transaction: profile, external identity, active flag and the outbox events they produce either all persist or none do. A `409 uniqueness` names the cause — a duplicate `userName` or an `externalId` that already belongs to another user — and leaves the user exactly as it was, with no events published.
- Deactivation (`active=false` via PUT/PATCH, or DELETE) is soft: the user keeps their UUID, history and role assignments, and **all their sessions are destroyed immediately** — deactivation in Okta locks the person out at once. Reactivation is supported; old sessions do not come back. How revocation works: [Architecture](ARCHITECTURE.md#sessions-and-revocation).
- Groups are not supported by design: role assignments live in this service only (see [Access control](ARCHITECTURE.md#access-control)).

## Entry proxy (Envoy ext-auth)

The proxy calls `/internal/session/validate` on the internal port for every protected request and forwards the `X-Identity-*` context headers upstream ([API](API.md#internal-listener-internal_listen_addr)). The chart renders the Gateway API resources ([Deployment](DEPLOYMENT.md#kubernetes)).

Contract details, each of which silently breaks the whole flow and each discovered only by wiring a real proxy:

- Envoy mirrors the **method** of the original request to the auth service, so a GET-only validate route rejects every POST;
- Envoy treats the configured auth path as a **prefix** and appends the original path (`/internal/session/validate/graphql`);
- Envoy sends the auth service only a small default header set — the session **cookie** must be listed in `headersToExtAuth`, otherwise validate never sees a session and denies everything;
- headers listed in `headersToBackend` **replace** the client's same-named headers, an empty value included — `stand:verify` sends forged `x-identity-*` headers with a valid session and checks the echo upstream saw the session's id and an empty permissions header (the qa user has no roles, which is exactly the case where "add if missing" semantics would leak a forged value).

## GraphQL Router and other subgraphs

For federation, the router propagates the session cookie to subgraphs (so this service authenticates router calls with its existing session check, no separate machine channel) and forwards the `X-Identity-*` context onward. A subgraph referencing our user must declare `type User @key(fields: "id", resolvable: false) { id: ID! }` — adding `@external` to the key field breaks composition.

The mutation `Origin` check ([API](API.md#graphql)) only works if the router forwards the browser's `Origin` header to the subgraph, so the production router configuration must propagate it (the stand's does). A subgraph that trusts the forwarded `x-identity-*` context must be reachable only from the router. **Put the GraphQL Router into `networkPolicy.public.extraFrom`** in the per-environment values — the router calls this subgraph directly, and without that peer federation stops at once.

## User events (RabbitMQ)

Every user change (create, profile update, deactivate/reactivate) writes an event into the `user_events_outbox` table **in the same transaction**; a relay inside `serve` publishes them to the durable topic exchange `identity.events` with publisher confirms. Routing keys: `user.created`, `user.updated`, `user.deactivated`, `user.reactivated`, `user.snapshot`.

The consumer contract — body schema, AMQP properties, delivery guarantees, what a consumer must do (deduplicate by `message_id`, apply the snapshot only when `user.version` is greater than the one already applied, treat every type as a full snapshot) and how to bootstrap or repair a projection with `replay-users` — lives in [`docs/events/user-events.md`](events/user-events.md). The JSON Schema and the per-type examples next to it are generated from the service's own payload code and checked by `go test ./internal/events`, so they cannot drift from what is published. `dev/stub-consumer/` is a reference consumer that follows the contract line by line and runs on the local stand. Delivery is at-least-once; events survive broker outages and service restarts in the outbox. The broker is deliberately excluded from `/readyz`.

**Topology contract.** This service declares only the durable topic exchange `identity.events`. Consumers declare their own durable queues and bind them (`user.#` or specific keys). Events are published as *mandatory*: an event no queue accepts is returned by the broker and **waits** — it is neither lost nor counted as a failed attempt (`outbox_unroutable_total`) — so deploying this service before its consumers is safe.

How the relay delivers: [Architecture](ARCHITECTURE.md#outbox-relay).
