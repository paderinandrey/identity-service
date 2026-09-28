# Playbook: the local stand

Use it to see the whole contour working — entry proxy with ext-auth, GraphQL Router, subgraphs, Keycloak as the IdP — or to prove a change that touches routing, ext-auth, the chart or federation.

A full end-to-end stand runs in a local cluster (OrbStack Kubernetes): Envoy Gateway with ext-auth, this service, its dependencies and Keycloak, all from the chart plus `values-local.yaml`.

## Steps

```bash
mise run stand:up       # images, Envoy Gateway, dependencies, migrations, service
mise run stand:verify   # proves the whole contour, step by step
mise run stand:down
```

Then sign in at <http://identity.localhost/auth/saml/init> as **qa@example.com / password** (the user ships in the imported realm and is provisioned into the database over SCIM by `stand:up`).

## How it is wired

Hostnames are `*.localhost` on purpose. A public domain pointing at loopback (localtest.me) looked equivalent but broke in the browser: OrbStack's DNS proxy rewrites loopback answers into its own 198.18.x range, which the browser cannot reach — while curl, going through the macOS resolver, got 127.0.0.1 and kept working, hiding the problem. Browsers resolve `*.localhost` themselves and treat it as a secure context, which also keeps Keycloak's `Secure` cookies working over plain HTTP. The verification script therefore drives the login through `scripts/stand-login.py`, which reproduces that browser cookie rule — curl drops `Secure` cookies over HTTP and cannot complete the flow.

Routing has two layers. **Envoy Gateway** routes by path and host and runs ext-auth: `/auth/*` and `/scim/v2/*` go straight to this service, `/graphql` goes to the router, every other service publishes its own HTTPRoute. The **Cosmo Router** (`charts/graphql-router`, its own release `identity-stand-router`) routes by GraphQL field across the composed supergraph; it belongs to no subgraph, which is why it is not a dependency of this chart. The stand composes three subgraphs — this service, a stub for GSH/DFM (`dev/stub-subgraph/`, owns `Order`) and a small messenger (`dev/messenger/`, owns `Message`, checks `messenger:messages.send` from the forwarded context and refuses mutations from a foreign `Origin`, as every cookie-authenticated subgraph must) — with `scripts/stand-compose-supergraph.sh`, which lists them in one place and fills the router's ConfigMap; adding a subgraph is one line there plus a stand dependency in `values-local.yaml`. Applications and roles of the stand come from `deploy/stand/access.yaml` (`seed-access`); the qa user deliberately has no roles, `stand-qa` holds `messenger/member`. A subgraph that trusts the forwarded `x-identity-*` context must be reachable only from the router: the stand gives messenger a NetworkPolicy admitting router pods alone, and `stand:verify` checks that a foreign pod with forged headers gets no connection.

## What `stand:verify` proves

`stand:verify` is the interesting part: it drives a real sign-in through the Keycloak login form and asserts, with observed values, that public routes stay open, internal paths are not published, an anonymous request to a protected path is refused **by the proxy** (`ext_authz_denied`, upstream never called), and that `X-Identity-*` context headers actually reach the protected upstream. The proxy contract details it guards are listed in [Integrations](../INTEGRATIONS.md#entry-proxy-envoy-ext-auth).

Steps 12–13 cover the internal zone: `/internal/*` is a 404 on the public port (12), the internal port refuses a pod outside the policy and serves a labeled one (13); step 14 is the forged-header check. The probe pods are long-lived (`sleep` + `kubectl exec`): the stand CNI adds a new pod to the policy's peer set asynchronously, so a one-shot `kubectl run -i` with the right labels was refused before its address was known.

## Checks

`mise run stand:verify` ends with `СТЕНД ПРОВЕРЕН ЦЕЛИКОМ`.
