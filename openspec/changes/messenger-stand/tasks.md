# messenger-stand — Tasks

## 1. Router как отдельный чарт

- [ ] 1.1 `charts/graphql-router` (Chart.yaml, values, Deployment, Service, helpers): образ, порт, readiness `/health`, ConfigMap по имени из values; `helm lint` и `helm template` чисты
- [ ] 1.2 identity-service: удалить `dependencies.router` из `values-local.yaml`; `protectedBackendRefs` → Service router; NetworkPolicy `public.extraFrom` → `app.kubernetes.io/name: graphql-router`; `ci-helm-checks.sh`: стенд без `router` в компонентах, отдельная секция lint+render для `charts/graphql-router`; `mise run chart:check` зелёный
- [ ] 1.3 `stand-compose-supergraph.sh`: список сабграфов в одном месте (identity с живого сервиса, orders и messenger из файлов), ConfigMap `identity-stand-graphql-router-config`, метки probe-пода; `stand-up.sh`: релиз router после композиции; `stand-down.sh`: удаляет оба релиза

## 2. Messenger

- [ ] 2.1 `dev/messenger`: модуль, схема (`Message`, `User @key(resolvable: false)`, `inbox`, `sendMessage`), gqlgen generate, `auth.go` (текущий пользователь и права из заголовков, `FORBIDDEN`/`UNAUTHENTICATED`), in-memory store, резолверы, Dockerfile; юнит-тесты: без пользователя — UNAUTHENTICATED, без права — FORBIDDEN и сообщение не сохранено, с правом — сохранено и видно в inbox получателя, не видно другим
- [ ] 2.2 CI-джоб `messenger` (vet + test), как у stub-consumer

## 3. Стенд

- [ ] 3.1 `deploy/stand/access.yaml`; `stand-up.sh`: сборка образа messenger, зависимость в `values-local.yaml`, шаг «Доступ» (seed-access + grant-role stand-qa → messenger/member); `mise run stand:up` зелёный
- [ ] 3.2 `stand-verify.sh`: шаг «третий сабграф» — sendMessage от stand-qa с `author.email` из identity, FORBIDDEN для qa без роли, inbox qa; `mise run stand:verify` зелёный целиком

## 4. Документация и приёмка

- [ ] 4.1 README: стенд — три сабграфа, router отдельным релизом, где живёт роутинг (Envoy: пути, Router: поля), как добавить сабграф
- [ ] 4.2 `gofmt`, `mise run lint`, `go test ./...`, тесты модулей `dev/*`, `chart:check`; CI зелёный
