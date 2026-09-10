# add-user-events — Design

## Context

Outbox-паттерн уже принят в экосистеме: GSH event_bus держит `outbox_events` (status, attempts, last_error, schema_version), публикует через Bunny с `message_id`/`correlation_uuid`/`event_type` и дедуплицирует на приёме через `inbox_events`. Наши изменения пользователей идут через методы `internal/postgres` (SCIM, CLI); сейчас это одиночные statement'ы без транзакций. Мотивация — proposal.md; требования — specs/user-events/spec.md.

## Goals / Non-Goals

**Goals**
- Атомарность «изменение + событие» и доставка at-least-once с версией против устаревших применений.
- Совместимость по духу с event_bus GSH (свойства сообщений, jsonb payload, счётчик попыток).
- Публикатор без внешнего оркестратора — горутина в `serve`.

**Non-Goals**
- Exactly-once и глобальный порядок (консьюмеры идемпотентны, версия отсекает устаревшее).
- События доступа/ролей; correlation-цепочки запросов (correlation_uuid оставляем пустым до появления сквозного контекста).
- Watermill/фреймворки — прямой amqp091-go достаточен.

## Decisions

1. **Схема.** `users.version bigint NOT NULL DEFAULT 1`; `user_events_outbox(id uuid pk default gen, event_type text, payload jsonb, attempts int default 0, last_error text, created_at, published_at nullable)` + частичный индекс `WHERE published_at IS NULL` по created_at. Статусной enum-машины GSH не заводим: `published_at IS NULL` + attempts достаточно для одного публикатора.
2. **Запись событий — внутри транзакций store.** Методы `CreateUser`, `UpdateUser`, `SetActive`, `UpsertByEmail` переводятся на `pgx.Tx`: изменение → `version = version + 1` (RETURNING) → INSERT в outbox. Хелпер `recordUserEvent(tx, type, user)`. `TouchLastSignIn` события не пишет. Тип события выбирается по факту: SetActive(false) → deactivated, SetActive(true) → reactivated, новый пользователь → created, иначе updated; UpsertByEmail существующего без изменения имени события не пишет.
3. **Payload** (schemaVersion 1, ключи в camelCase как в GraphQL):
   `{"id": outboxID, "type": "identity.user.updated", "schemaVersion": 1, "occurredAt": RFC3339, "user": {"id","email","name","active","version"}}`.
4. **Публикатор** (`internal/events.Relay`): цикл с интервалом 1s (конфигурируемым не делаем — YAGNI): `SELECT ... WHERE published_at IS NULL ORDER BY created_at LIMIT 100 FOR UPDATE SKIP LOCKED` в транзакции; для каждого — publish с confirm; успешные помечаются `published_at = now()`, неуспешные — `attempts+1, last_error` (та же транзакция), после чего пауза с экспоненциальным backoff (до 30s) при ошибке соединения. Порядок по пользователю сохраняется сортировкой по created_at и единственным экземпляром публикатора (replica=1; при горизонтальном масштабировании SKIP LOCKED делит пакеты — допустимо, версии защищают).
5. **AMQP**: `amqp091-go` v1.14; одно соединение с автопереподключением в цикле relay (пересоздание при ошибке), durable topic exchange `identity.events` (declare при старте), routing key = `event_type` без префикса `identity.` (`user.updated`), properties: `MessageId`, `Type`, `ContentType: application/json`, `DeliveryMode: Persistent`, `Timestamp`.
6. **Конфигурация**: `RABBITMQ_URL` (dev default `amqp://identity:identity@localhost:5673/`, обязателен вне development), `EVENTS_EXCHANGE` (default `identity.events`). В development при недоступном брокере relay логирует и ретраит — сервис не падает; на старте fail-fast не делаем (брокер — не критическая зависимость запуска, outbox копится). Readiness БРОКЕР НЕ включает — деградация доставки не должна выводить под из балансировки пользовательского трафика.
7. **docker-compose**: `rabbitmq:4.2-management`, host-порты 5673 (amqp; 5672 свободен, но держим единый стиль нестандартных портов) и 15673 (management UI), учётка identity/identity, healthcheck `rabbitmq-diagnostics ping`.
8. **Replay** — `replay-users`: батчами по 500 пользователей вставляет snapshot-события в outbox (одной транзакцией на батч). Не трогает published_at существующих.
9. **Тесты**: unit на выбор типа события; интеграционные с PostgreSQL — атомарность/версии/replay; интеграционные с RabbitMQ (skip при недоступности, как остальные) — доставка, свойства сообщения, добор после «падения» брокера (закрытие соединения в тесте), публикация после рестарта relay.

## Risks / Trade-offs

- [At-least-once → дубликаты у консьюмеров] → Контракт: стабильный `message_id` + inbox-дедуп (у GSH уже есть) + версия профиля.
- [Разовый publish-confirm на сообщение медленнее батчей] → На наших объёмах (кадровые изменения) несущественно; батчирование confirm — оптимизация по необходимости.
- [Один публикатор — единая точка задержки] → Outbox переживает всё; задержка доставки не ломает корректность. Мониторинг: лог ошибок + attempts в таблице.
- [Перевод store-методов на транзакции затрагивает существующие тесты] → Поведенческие контракты не меняются; тесты дополняются проверкой событий.

## Migration Plan

Миграция добавляет колонку с DEFAULT и новую таблицу — совместимо с rolling. Порядок включения: migrate → deploy → (консьюмеры готовы) → `replay-users` для первичной загрузки. Rollback: goose down (события теряются — до появления консьюмеров безопасно).

## Open Questions

- Точные имена очередей/binding'ов на стороне GSH/DFM — их зона; контракт exchange+routing key фиксируется этой спекой.
