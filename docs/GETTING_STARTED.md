# Getting started

From a fresh clone to a signed-in session on your machine.

## Prerequisites

- [mise](https://mise.jdx.dev/) — pins Go, golangci-lint and tbls (see `mise.toml`)
- Docker — local PostgreSQL, Redis, RabbitMQ, Keycloak and image builds

Non-default host ports 5433/6380 are used because 5432/6379 are commonly taken by other local projects.

## Steps

1. Install the pinned tools and start the infrastructure:

   ```bash
   mise install
   mise run up         # PostgreSQL (5433), Redis (6380), RabbitMQ (5673, UI on 15673)
   ```

2. Build and migrate:

   ```bash
   mise run build      # bin/identity-service
   bin/identity-service migrate
   ```

3. Run the service:

   ```bash
   mise run run        # serve is the default subcommand
   ```

   Without `SAML_IDP_METADATA_URL` the service starts with SSO routes disabled (development convenience).

## You are done when

```bash
curl -s localhost:8080/readyz
# {"status":"ok"}
```

To sign in for real, start Keycloak as the local IdP — see [Local SSO with Keycloak](DEVELOPMENT.md#local-sso-with-keycloak). The full contour (Envoy ext-auth, GraphQL Router, subgraphs) runs on [the local stand](playbooks/LOCAL_STAND.md).

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `/readyz` answers 503 | PostgreSQL or Redis is not reachable; `mise run up` |
| `/auth/saml/init` is a 404 | SSO routes are not mounted without `SAML_IDP_METADATA_URL`; follow the Keycloak section |
| sign-in answers 401 for a user who exists | there is no sign-in by email: the user must be provisioned over SCIM with the IdP's id as `externalId` |
