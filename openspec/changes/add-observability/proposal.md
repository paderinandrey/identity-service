# add-observability

## Why

Сервис держит вход всей экосистемы, но виден операторам только через JSON-логи: ошибки не агрегируются, паники не репортятся, метрик нет — деградацию (растущий outbox, медленный validate, промахи кешей) заметить нечем. В GSH Sentry и метрики уже стандарт; identity-service должен соответствовать до подключения реального трафика.

## What Changes

- **Sentry** (`sentry-go` v0.49): инициализация по `SENTRY_DSN` (опционален во всех окружениях — без DSN интеграция выключена), environment из `APP_ENV`, flush при shutdown. Захват: паники HTTP-хендлеров (recovery-middleware с 500 и репортом), записи slog уровня Error через официальный `sentryslog`-мост (ошибки relay, SAML, SCIM попадают в Sentry автоматически, категории без чувствительных данных сохраняются).
- **Prometheus** (`client_golang` v1.24): endpoint `GET /internal/metrics` (внутренний, как validate), стандартные Go/process-коллекторы плюс:
  - HTTP: гистограмма длительности и счётчик запросов по маршруту (`http.Request.Pattern`), методу и классу статуса;
  - события: `outbox_pending` (gauge), `events_published_total`, `event_publish_errors_total`;
  - аутентификация: `sign_ins_total` (успех/отказ), `scim_operations_total` по типу операции;
  - кеши: `cache_requests_total{cache, result=hit|miss}` для прав и признака активности.
- Recovery-middleware также закрывает пробел: сейчас паника хендлера роняет соединение без ответа и без следа.

Не входит: трейсинг (OTel), алерты и дашборды, метрики Redis/PostgreSQL-пулов (есть стандартные экспортёры), Sentry в CLI-подкомандах.

## Capabilities

### New Capabilities

- `observability`: отчёты об ошибках в Sentry, Prometheus-метрики, восстановление после паник.

### Modified Capabilities

Нет: существующее поведение не меняется, добавляются наблюдательные поверхности.

## Impact

- Новый пакет `internal/observability` (Sentry-инициализация, recovery, metrics-registry, HTTP-middleware); инструментирование в `internal/events` (relay), `internal/samlsso`, `internal/scim`, кешах `identity`/`access` — через маленькие интерфейсы-счётчики, без прямой зависимости capability-пакетов от prometheus.
- Новые зависимости: `github.com/getsentry/sentry-go` (+ `/slog`), `github.com/prometheus/client_golang`.
- Конфигурация: `SENTRY_DSN` (опционален). README: раздел observability.
