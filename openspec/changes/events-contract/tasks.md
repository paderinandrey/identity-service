# events-contract — Tasks

## 1. Контракт и golden-тесты

- [x] 1.1 `docs/events/user-event.schema.json` (JSON Schema 2020-12, строгая: enum типов, uuid, date-time, `additionalProperties: false`) и `docs/events/user-events.md` (exchange, routing keys, свойства AMQP, семантика типов, обязанности консьюмера с SQL-образцом `UPDATE … WHERE version < $new`, replay, что считать ошибкой); проверить `mise run lint` не трогает docs
- [x] 1.2 `events.go`: часы пакета, подменяемые в тестах; `internal/events/contract_test.go` с `santhosh-tekuri/jsonschema/v6` (test-only в `go.mod`): payload каждого типа проходит схему; `docs/events/examples/<type>.json` равен payload байт-в-байт, флаг `-update` перезаписывает; сгенерировать примеры и проверить, что удаление поля из схемы или примера роняет тест
- [x] 1.3 README «User events» ссылается на `docs/events/` вместо встроенного JSON; `go test ./internal/events/` зелёный

## 2. Референсный консьюмер

- [x] 2.1 `dev/stub-consumer` (модуль, Dockerfile по образцу stub-subgraph): `apply.go` — чистая `Apply(state, event)` с исходами applied/duplicate/stale/rejected; юнит-тесты на дубликат по id, устаревшую версию, параллельные N/N+1 в обоих порядках, невалидный JSON, неизвестный `type` как снимок
- [x] 2.2 `consume.go`: своя durable-очередь `stub-consumer.users`, binding `user.#` к `identity.events`, prefetch, ack для applied/duplicate/stale, nack без requeue для rejected, переподключение с backoff; `http.go`: `/healthz`, `/projection`, `/projection/{id}`, `/stats`; `go vet` и `go test` модуля зелёные
- [ ] 2.3 CI: джоб или шаг, гоняющий `go test` в `dev/stub-consumer` (как для основного модуля), чтобы референс не гнил молча

## 3. Стенд

- [ ] 3.1 `values-local.yaml`: зависимость `stub-consumer`; `stand-up.sh` собирает `stub-consumer:dev`; `ci-helm-checks.sh` ждёт компонент в рендере стенда; `mise run chart:check` зелёный
- [ ] 3.2 `stand-verify.sh`: шаг после SCIM-провижининга — `replay-users` → проекция содержит всех пользователей с версиями из БД; SCIM PATCH имени → проекция обновилась с версией +1; повторный `replay-users` → `stats.stale` вырос, проекция без изменений; нумерация шагов сдвинута
- [ ] 3.3 `mise run stand:up` и `mise run stand:verify` зелёные целиком

## 4. Приёмка

- [ ] 4.1 `gofmt`, `mise run lint`, `go test ./...` (CGO off локально), `mise run chart:check`; итоговый diff просмотрен — `relay.go`/`publisher.go`/миграции не тронуты; CI зелёный
