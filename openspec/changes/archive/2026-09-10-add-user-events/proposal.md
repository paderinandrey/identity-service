# add-user-events

## Why

GSH и DFM держат локальные проекции пользователей (списки сотрудников, назначения задач, `audited_by`), но сейчас узнать об изменениях профилей и деактивациях им неоткуда. Архитектурное решение предписывает: transactional outbox в PostgreSQL → RabbitMQ → локальные проекции; обработка идемпотентна, устаревшие версии не применяются, есть первоначальная загрузка и восстановление пропущенного. Outbox-паттерн уже принят в экосистеме (event_bus в GSH) — публикуем в совместимых конвенциях.

## What Changes

- Таблица `user_events_outbox` (id, event_type, payload jsonb, attempts, last_error, created_at, published_at): события записываются **в одной транзакции** с изменением пользователя.
- Версия профиля: колонка `users.version`, инкрементируется при каждом изменении профиля или признака активности; входит в payload — консьюмеры отбрасывают устаревшие обновления.
- События: `identity.user.created`, `identity.user.updated`, `identity.user.deactivated`, `identity.user.reactivated`, `identity.user.snapshot` (для загрузки/восстановления). Payload: id события, тип, schemaVersion, occurredAt и снимок пользователя (uuid, email, имя, active, version). Источники: SCIM (create/update/deactivate/reactivate), CLI `create-user`.
- Relay-публикатор в процессе `serve`: опрос outbox (`FOR UPDATE SKIP LOCKED`), публикация в durable topic-exchange `identity.events` (routing key = тип события) с publisher confirms и свойствами сообщения по конвенциям GSH (`message_id` = id события, `type`, персистентность); backoff и счётчик попыток при сбоях, публикация переживает рестарт.
- CLI `replay-users`: постановка snapshot-событий всех пользователей в outbox — первоначальная загрузка проекции и восстановление после потерь.
- Инфраструктура: RabbitMQ в docker-compose; конфигурация `RABBITMQ_URL` (обязателен вне development; в development без него публикация выключена с предупреждением, outbox продолжает наполняться) и `EVENTS_EXCHANGE`.

Не входит: консьюмеры на стороне GSH/DFM (их inbox-дедупликация уже существует как паттерн), события изменений доступа (роли — отдельное решение), гарантия порядка между разными пользователями.

## Capabilities

### New Capabilities

- `user-events`: события изменений пользователей — транзакционный outbox, версия профиля, публикация в RabbitMQ, replay.

### Modified Capabilities

Нет: требования существующих capabilities не меняются (версия профиля — часть контракта событий; поведение API и провижининга внешне прежнее).

## Impact

- Новые: миграция (outbox + `users.version`), пакет `internal/events` (типы событий, relay), запись событий в методах `internal/postgres` (переход на транзакции), CLI-подкоманда.
- Новая зависимость: `github.com/rabbitmq/amqp091-go` v1.14.0 (зафиксирована исследованием стека).
- docker-compose: контейнер RabbitMQ (management-порт для отладки); README.
- Консьюмерам (задел): контракт события фиксируется в спеке — стабильный id, тип, schemaVersion, версия профиля.
