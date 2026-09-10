# add-user-events — Tasks

## 1. Схема и запись событий

- [x] 1.1 Миграция: `users.version` (DEFAULT 1) и `user_events_outbox` с частичным индексом непубликованных, COMMENT ON; `migrate` применяет, откат проходит на тестовой БД
- [x] 1.2 Пакет `internal/events`: типы событий, payload-структура, выбор типа по изменению; unit-тесты выбора типа
- [x] 1.3 Перевести `CreateUser`/`UpdateUser`/`SetActive`/`UpsertByEmail` на транзакции с инкрементом версии и записью события; интеграционные тесты: событие в одной транзакции (откат не оставляет), версия монотонна, `TouchLastSignIn` и no-op upsert событий не пишут

## 2. Публикация

- [x] 2.1 docker-compose: RabbitMQ 4.2-management (host 5673/15673, healthcheck); `mise run up` — контейнер healthy
- [x] 2.2 Конфигурация `RABBITMQ_URL` (dev default, обязателен вне development) и `EVENTS_EXCHANGE`; unit-тесты
- [x] 2.3 `events.Relay`: опрос outbox FOR UPDATE SKIP LOCKED, publish с confirm, пометка published/attempts+last_error, переподключение с backoff; запуск в `serve`; интеграционные тесты с RabbitMQ: доставка с корректными properties (message_id, type, persistent), добор после недоступности брокера, публикация накопленного после рестарта relay
- [x] 2.4 Порядок по пользователю: тест — три изменения одного пользователя приходят в порядке возрастания версии

## 3. Replay и завершение

- [x] 3.1 CLI `replay-users`: snapshot-события всех пользователей батчами; интеграционный тест: события с текущими версиями появляются в outbox и доставляются
- [x] 3.2 Прогнать `mise run lint`, `mise run test`, `mise run vuln`; все чистые
- [x] 3.3 README: раздел событий (exchange, routing keys, контракт payload, replay, RABBITMQ_URL), обновить статус; вычитать
