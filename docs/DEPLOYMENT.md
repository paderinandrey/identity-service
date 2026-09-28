# Deployment

## Docker

```bash
docker build -t identity-service .
docker run --rm -p 8080:8080 -p 8081:8081 identity-service   # serve (public + internal zone)
docker run --rm identity-service migrate                 # init container
```

The image is multi-stage: static binary (`CGO_ENABLED=0`) on `gcr.io/distroless/static-debian12:nonroot`. The process handles SIGTERM itself (it runs as PID 1) and shuts down gracefully.

## Kubernetes

`charts/identity-service/` is a Helm chart following the conventions of the other services in the ecosystem (in-repo chart, per-environment values supplied by ArgoCD). Schema migrations run as a `pre-install`/`pre-upgrade` hook (`argocd.argoproj.io/sync-wave: "-1"`) that brings its own ServiceAccount and ConfigMap — pre-install hooks run before any regular resource of the release exists, so a hook that referenced the release's own objects could never start on a first install. `stand:verify` proves the first install and an upgrade for real: chart into an empty namespace against an external database. The hook finishes before new pods start. Probes use the service contract paths `/healthz` and `/readyz`.

Non-secret configuration is rendered into a ConfigMap from `config` in values; connection strings, tokens and the RelayState secret must come from an existing Secret named in `existingSecret` — never from values. Every variable: [CONFIGURATION.md](CONFIGURATION.md).

With `gateway.enabled=true` the chart also renders Gateway API resources: two HTTPRoutes (sign-in and provisioning paths stay public; `/graphql` is protected) and an Envoy Gateway `SecurityPolicy` whose ext-auth calls this service's own `/internal/session/validate` and forwards the `X-Identity-*` context headers upstream. Internal paths are deliberately not routed from outside.

The Service exposes two named ports, `http` (public zone) and `internal` (validation, metrics, e2e login); the ext-auth SecurityPolicy and the ServiceMonitor use `internal`, HTTPRoutes only ever use `http`. A **NetworkPolicy** (`networkPolicy.enabled`, on by default; ingress only) admits the proxy namespace (`networkPolicy.gatewayNamespace`) to both ports, the monitoring namespace to `internal`, and whatever is listed in `networkPolicy.public.extraFrom` / `internal.extraFrom`. **Put the GraphQL Router into `public.extraFrom`** in the per-environment values — the router calls this subgraph directly, and without that peer federation stops at once. Egress is not restricted: dependency addresses belong to the environment. On a CNI without NetworkPolicy enforcement the policy is inert; the stand (k3s under OrbStack) enforces it and `stand:verify` proves both the isolation and the header overwrite.

Changing either port on a running environment needs two releases: first an image that serves the zone on both the old and the new port, then the chart switch of SecurityPolicy/ServiceMonitor — a single release would point ext-auth at old pods that do not listen there yet.

```bash
mise run chart:check   # lint + render checks (the same script CI runs)
```

The whole contour runs locally on [the stand](playbooks/LOCAL_STAND.md).
