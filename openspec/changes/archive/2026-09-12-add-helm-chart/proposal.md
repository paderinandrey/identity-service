# add-helm-chart

## Why

Сервис не разворачивается никуда, кроме docker-compose. Реальный деплой экосистемы — ArgoCD → EKS, причём GSH держит Helm-чарт прямо в репозитории (`charts/gsh-service/`), а ArgoCD подставляет per-environment values. Чтобы identity-service вообще можно было задеплоить — и локально, и на стенд, и позже на EKS — нужен свой чарт по той же конвенции.

## What Changes

- `charts/identity-service/` по образцу GSH: Deployment (HTTP-сервер), Service, Job миграций (`identity-service migrate`) как хук перед апгрейдом, ConfigMap для несекретной конфигурации и ссылки на Secret для токенов/DSN, liveness/readiness probes на существующие `/healthz` и `/readyz`, HPA, PDB, ServiceMonitor для `/internal/metrics`.
- Шаблоны Gateway API за флагом (`gateway.enabled`): HTTPRoute для `/auth/*`, `/scim/v2/*`, `/graphql` и SecurityPolicy с ext-auth на защищённые пути. Держим их в чарте, чтобы конфигурация входа versioned вместе с сервисом и переносилась на EKS без переписывания.
- Внутренние пути (`/internal/*`) не публикуются наружу маршрутами — доступны только внутри кластера.
- `helm lint` и `helm template` с несколькими наборами values прогоняются в CI.

Не входит: ArgoCD Application (живёт в инфраструктурном репозитории), per-environment values, managed-инстансы БД.

## Capabilities

Нет изменений поведения сервиса (`skip_specs: true`).

## Impact

Новый каталог `charts/identity-service/`, джоб в CI, README. Кода сервиса не касается.
