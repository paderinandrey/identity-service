# internal-listener

## Why

Внутренние маршруты — проверка сессии для ext-auth, метрики и e2e-вход —
живут на том же порту и в том же mux, что публичные `/auth/*`, `/scim/v2/*`
и `/graphql`. Снаружи их спасает только то, что HTTPRoute не публикует
`/internal/`; внутри кластера любой под может вызвать validate, а
NetworkPolicy в чарте нет. Codex (обзор 14.09) назвал это главным
нерешённым вопросом безопасности: заголовки `X-Identity-*` не подписаны,
и «клиент не может подделать контекст» держится на конфигурации сети,
которая нигде не зафиксирована. Пункт 12 волны 3 в
[[2026-09-14-triage-and-plan]]; жёсткая предпосылка интеграции с GSH
([[2026-09-18-gsh-integration]]): GSH не срезает входящие заголовки, и
без gateway с перезаписью доверенный контекст — обход авторизации.

Сейчас, потому что это единственный пункт волны 3, который целиком
делается в репозитории, и потому что OrbStack-стенд, как выяснилось,
применяет NetworkPolicy (проверено: deny-all блокирует соединение) — есть
где это доказать.

## What Changes

- **Второй HTTP listener** для внутренней зоны: `INTERNAL_LISTEN_ADDR`
  (по умолчанию `:8081`). На него переезжают `/internal/session/validate`,
  `/internal/metrics` и `/internal/e2e/login`. На публичном адресе
  `/internal/*` больше нет (404). Health/readiness остаются на публичном
  адресе — их зовёт kubelet, а не proxy. **BREAKING** для тех, кто зовёт
  validate или скрейпит метрики по порту 8080: стенд-скрипты и чарт
  переводятся в этом же change.
- **Чарт**: Service с двумя портами (`http`, `internal`); ext-auth в
  SecurityPolicy и ServiceMonitor указывают на `internal`; Deployment
  объявляет второй containerPort. HTTPRoute не меняется.
- **NetworkPolicy в чарте** (ingress): внутренний порт — только из
  namespace gateway и мониторинга (плюс явный список peers из values);
  публичный порт — только из namespace gateway и от router'а. Egress не
  ограничивается: зависимости managed, их адреса — свойство окружения.
- **Требование к контуру**: заголовки `X-Identity-*`, дошедшие до
  upstream, SHALL быть теми, что выдал validate, а не теми, что прислал
  клиент. Envoy перезаписывает `headersToBackend` ответом ext-auth; это
  свойство фиксируется в спеке и доказывается стендом, включая случай
  пользователя без ролей (пустой заголовок прав должен вытеснить
  подделанный).
- **Стенд**: `stand-compose-supergraph.sh` берёт e2e-сессию с внутреннего
  порта из пода с меткой, которую пропускает политика; `stand-verify`
  получает шаги «validate недоступен на публичном порту», «validate с
  внутреннего порта недоступен из немаркированного пода и доступен из
  разрешённого», «подделанные `x-identity-*` от клиента перезаписаны».
- `ci-helm-checks.sh`: NetworkPolicy рендерится, ext-auth и ServiceMonitor
  смотрят на внутренний порт.

## Capabilities

### New Capabilities

Нет.

### Modified Capabilities

- `service-runtime`: конфигурация получает адрес внутреннего listener'а;
  новое требование о двух зонах — внутренние маршруты обслуживаются
  только на внутреннем адресе, публичный адрес их не знает.
- `web-sessions`: «Внутренняя проверка сессии» — endpoint обслуживается
  только внутренним listener'ом; контекст, переданный upstream, заменяет
  одноимённые заголовки исходного запроса.
- `observability`: `/internal/metrics` — на внутреннем listener'е.
- `e2e-login`: `/internal/e2e/login` — на внутреннем listener'е.

## Impact

- `internal/config` (новая переменная), `internal/httpserver` (два сервера
  с общим graceful shutdown), `cmd/identity-service/main.go` (разводка
  маршрутов по зонам).
- Чарт: `service.yaml`, `deployment.yaml`, `securitypolicy.yaml`,
  `servicemonitor.yaml`, новый `networkpolicy.yaml`, `values.yaml`,
  `values-local.yaml`, `ci-helm-checks.sh`.
- Стенд: `stand-compose-supergraph.sh`, `stand-verify.sh`.
- README: зоны, порты, NetworkPolicy.
- Совместимость: rolling-раскат безопасен — новый Service/SecurityPolicy
  применяются одним релизом; старые поды без порта 8081 недоступны для
  ext-auth ровно до их замены, readiness новых подов не зависит от
  политики. Внешние скрейперы метрик по 8080 после раската перестанут
  работать — на это и рассчитано.
