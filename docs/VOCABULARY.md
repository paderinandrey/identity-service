# Vocabulary

The words below mean one thing in code, specs, tickets, pull requests and agent prompts. Use them as written; when a new term appears, add it here before it spreads.

## Terms

| Term | Means | Not to be confused with |
| --- | --- | --- |
| **User** | the person inside the ecosystem, identified by a stable UUID (the *global id*) that every service uses | an Okta account or an email address |
| **Global id** | the user's UUID issued by this service; the only key other services store | the IdP subject |
| **Email** | a mutable attribute of the user, written only by SCIM | an identity key — there is no sign-in or linking by email |
| **External identity** | a mapping `(provider, subject) → user`; the provider is `okta` | the user itself |
| **Subject** | the IdP's immutable user id: Okta `user.id`, sent as the persistent SAML NameID and as SCIM `externalId` | the email or the NameID *format* |
| **Provisioning** | creating, updating, deactivating users through SCIM from the IdP | signing in |
| **Deactivation** | soft switch-off of a user: id, history and assignments stay, sessions are revoked at once | deletion |
| **Application** | a product that owns roles and permissions (`identity`, `gsh`, `dfm`, …) | a service deployment |
| **Permission** | an action inside an application, written `app:permission` (`identity:access.manage`) | a role |
| **Role** | a named set of an application's permissions, written `app/role` (`gsh/sourcing_manager`) | a group in the IdP |
| **Assignment** | a role held by a user; the only source of truth for access, changed only in this service | a projection of roles in another service |
| **Access audit log** | the append-only journal of access changes with actor, target, action | application logs |
| **Session** | server-side state in Redis behind an opaque id in an HttpOnly cookie | a token the browser can read |
| **Session epoch** | the user's revocation generation; a session older than the current epoch is refused | the session's expiry |
| **Public / internal zone** | the two listeners: public routes and probes; validation, metrics, e2e login | environments |
| **Context headers** | `X-Identity-User-Id`, `X-Identity-Email`, `X-Identity-Permissions` set by the entry proxy after validation | headers a client may send — those are overwritten |
| **User event** | a full user snapshot published to `identity.events` (`user.created`, `user.updated`, …) | a diff or a command |
| **Outbox** | `user_events_outbox`: events written in the transaction of the change | a message queue |
| **Relay** | the loop inside `serve` that publishes outbox rows under leases | a consumer |
| **Quarantine** | an outbox row parked after exhausting its retry budget; `requeue-events` returns it | a dead-letter queue owned by consumers |
| **Projection** | a consumer's local copy of users, built from user events | an authoritative source of access |
| **Replay** | `replay-users`: enqueue a snapshot of every user to (re)build projections | redelivery of old messages |

## Words to avoid

| Instead of | Say |
| --- | --- |
| "login by email", "link accounts by email" | nothing — the service never does it; users are matched by subject |
| "user groups" | roles (groups are not supported by design) |
| "delete a user" | deactivate |
