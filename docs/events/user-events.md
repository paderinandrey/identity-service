# User events: the consumer contract

identity-service publishes every user change to RabbitMQ so that
business services (GSH, DFM, later ERP and Messenger) can keep a local
projection of users — id, email, name, active flag — without reading this
service's database or calling it on the request path. This document is
the contract a consumer is written against. The body schema is
`user-event.schema.json`; the examples in `examples/` are generated from
the service's own code and checked in CI, so they never drift from what
is actually published.

## Transport

| Property | Value |
| --- | --- |
| Exchange | `identity.events`, type `topic`, durable |
| Routing key | event type without the `identity.` prefix: `user.created`, `user.updated`, `user.deactivated`, `user.reactivated`, `user.snapshot` |
| Body | JSON, `user-event.schema.json`, `Content-Type: application/json` |
| `message_id` | equals body `id` — the deduplication key |
| `type` | equals body `type` |
| Delivery mode | persistent; publisher confirms on the producer side |

Consumers own their queues. Declare a durable queue named after your
service (for example `gsh.identity.users`), bind it to `identity.events`
with `user.#`, and keep it bound: a message published while no queue is
bound is returned to the producer as unroutable and waits in its outbox,
it is not lost — but it is not delivered either until the binding exists.

## Delivery guarantees

- **At-least-once.** The producer writes the event in the same database
  transaction as the user change and publishes it afterwards; a crash
  between the two republishes the same event with the same `id`.
- **No ordering guarantee across messages.** Events of one user are
  produced in version order, but redelivery, multiple relay replicas and
  a consumer's own parallelism can reorder them. Never rely on arrival
  order; rely on `user.version`.
- **Every event carries the full user snapshot.** There are no partial
  updates, so applying any event is "replace the projection row with this
  snapshot if it is newer".

## What a consumer must do

1. **Deduplicate by `id`.** Keep the ids you have processed (an inbox
   table with a unique index on the event id is the usual shape).
   A repeated `id` is acknowledged and ignored.
2. **Apply by version, atomically.** Write the snapshot only if
   `user.version` is greater than the version stored for that user, in
   the same statement or transaction that stores it:

   ```sql
   INSERT INTO identity_users (id, email, name, active, version)
   VALUES ($1, $2, $3, $4, $5)
   ON CONFLICT (id) DO UPDATE
     SET email = EXCLUDED.email, name = EXCLUDED.name,
         active = EXCLUDED.active, version = EXCLUDED.version
     WHERE identity_users.version < EXCLUDED.version;
   ```

   An event whose version is not greater is *stale*: acknowledge it,
   count it, change nothing. Two events of one user processed at the
   same time in either order end with the higher version applied.
3. **Treat every type the same way.** `created`, `updated`,
   `deactivated`, `reactivated` and `snapshot` all carry the same body;
   the type is for routing, metrics and for reacting to a deactivation
   (for example closing the user's live connections) — not for deciding
   whether to apply the snapshot. A type you do not know is still a
   snapshot: apply it. That is how new types stay compatible.
4. **Reject what you cannot parse.** A body that is not valid JSON, does
   not match the schema or has an unknown `schemaVersion` is rejected to
   a dead-letter queue (nack without requeue) and reported; retrying it
   cannot help.
5. **Keep the user id as the key.** Email changes; the id never does.
   Existing local users are linked to the global id once, during the
   initial load, never by email at runtime.

## Initial load and recovery

`bin/identity-service replay-users` enqueues a `user.snapshot` event for
every user with its current version. Run it to bootstrap an empty
projection or to repair one after lost messages: snapshots of users you
already have at the same version are stale and ignored, users you are
missing are created, users that changed while you were down are brought
up to date. Replay is safe to run any number of times.

## Reference implementation

`dev/stub-consumer/` is a small Go consumer that follows this document
line by line: `apply.go` holds the rules as a pure function with tests
for duplicates, stale versions and concurrent events; `consume.go` is
the AMQP side (own durable queue, `user.#` binding, ack/nack policy).
It runs on the local stand, where `stand:verify` proves replay into an
empty projection, a live SCIM change reaching the projection and a
second replay being ignored as stale. It is a reference for behaviour,
not a library: its projection lives in memory.

## Changing the contract

Adding an optional field to `user` is a compatible change: the schema,
the examples and this document are updated together, `schemaVersion`
stays `1`, and consumers that do not know the field ignore it (the
published schema forbids unknown fields only for the producer's own
check). Removing or renaming a field, or changing a type, bumps
`schemaVersion` and is announced to every consumer before it ships.
