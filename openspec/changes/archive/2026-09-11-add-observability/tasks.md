# add-observability — Tasks

## 1. Основа

- [x] 1.1 Конфигурация `SENTRY_DSN` (опционален); пакет `internal/observability`: Sentry-init с environment, `Shutdown` с flush, tee-slog-хендлер (всё → JSON, error → sentryslog); unit-тесты с мок-транспортом: error-запись уходит в Sentry, без DSN — no-op
- [x] 1.2 Recovery-middleware: 500 + лог + Sentry-репорт, обёртка всего mux в `httpserver`; тесты: паника хендлера → 500, следующий запрос обслуживается, событие в мок-транспорте
- [x] 1.3 Prometheus-registry (Go/process-коллекторы), HTTP-middleware (гистограмма+счётчик по route/method/status_class из `r.Pattern`), endpoint `GET /internal/metrics`; тест: после запросов скрейп содержит метки маршрутов

## 2. Инструментирование

- [x] 2.1 Метрики relay: интерфейс `RelayMetrics`, published/errors счётчики и pending-gauge (обновление раз в цикл); интеграционный тест с RabbitMQ: значения растут/спадают согласно спеке
- [x] 2.2 Счётчики входов SSO (успех/отказ по категории) и SCIM-операций; тесты в существующих окружениях samlsso/scim
- [x] 2.3 Кеш-метрики hit/miss для `ActiveChecker` и `PermissionsCache` (nil-safe интерфейс); unit-тесты hit и miss

## 3. Завершение

- [x] 3.1 Wiring в `serve`: observability до остальных компонентов, Shutdown после остановки сервера; smoke на живом сервере: `/internal/metrics` отвечает, паника тестового хендлера не роняет процесс
- [x] 3.2 Прогнать `mise run lint`, `mise run test`, `mise run vuln`; все чистые
- [x] 3.3 README: раздел observability (`SENTRY_DSN`, `/internal/metrics`, перечень метрик); вычитать
