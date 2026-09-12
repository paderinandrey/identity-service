# add-helm-chart — Tasks

## 1. Чарт

- [x] 1.1 Скелет `charts/identity-service/` (Chart.yaml, values.yaml, _helpers.tpl) по конвенциям `charts/gsh-service`; `helm lint` проходит
- [x] 1.2 Deployment + Service: образ, порт, env из ConfigMap/Secret, probes на `/healthz` и `/readyz`, resources, securityContext (nonroot, read-only rootfs)
- [x] 1.3 Job миграций (`identity-service migrate`) как pre-install/pre-upgrade hook с `backoffLimit`; проверить рендер и порядок хуков
- [x] 1.4 HPA, PDB, ServiceMonitor на `/internal/metrics`; каждый за своим флагом в values

## 2. Вход (Gateway API)

- [x] 2.1 HTTPRoute за флагом `gateway.enabled`: публичные `/auth/*` и `/scim/v2/*`, защищённый `/graphql`; `/internal/*` наружу не публикуется
- [x] 2.2 SecurityPolicy с ext-auth на `/graphql`: backend — сам сервис, путь `/internal/session/validate`, проброс `X-Identity-User-Id/Email/Permissions` в upstream

## 3. Завершение

- [ ] 3.1 CI-джоб: `helm lint` + `helm template` с default и с `gateway.enabled=true`; зелёный
- [x] 3.2 README: раздел о чарте и о том, что per-environment values задаёт ArgoCD
