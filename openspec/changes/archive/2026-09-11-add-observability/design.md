# add-observability — Design

## Context

Логи уже структурные (slog JSON), health/readiness есть; нет агрегирования ошибок, паник-рекавери и метрик. Sentry-конвенции экосистемы — категория события + контекст без чувствительных данных (как в SCIM-аутентификаторе GSH). Мотивация — proposal.md; требования — specs/observability/spec.md.

## Goals / Non-Goals

**Goals**
- Ошибки и паники видны в Sentry без изменения существующих логов-вызовов (slog-мост).
- Метрики без загрязнения capability-пакетов зависимостью от prometheus — инъекция счётчиков интерфейсами.
- Нулевые накладные при выключенных Sentry/скрейпе.

**Non-Goals**
- OTel-трейсинг, кастомные дашборды/алерты, exemplars.
- Метрики в CLI-подкомандах; Sentry в CLI (ошибки CLI видны оператору напрямую).

## Decisions

1. **Пакет `internal/observability`** — единственное место, знающее о sentry-go и prometheus:
   - `Init(cfg, logger) (*Observability, error)`: Sentry init (если DSN задан) + registry с `collectors.NewGoCollector`, `NewProcessCollector`; метод `Shutdown(ctx)` — `sentry.Flush(2s)`.
   - `Logger(base slog.Handler) *slog.Logger`: оборачивает JSON-хендлер в `slog.NewMultiHandler`-подобный fanout (наш маленький tee-handler): всё — в JSON, `>= Error` — в `sentryslog`-хендлер. Существующие вызовы `logger.Error(...)` начинают репортиться без правок по коду.
   - `Recover(next http.Handler) http.Handler`: recover → `sentry.CurrentHub().Recover` + лог + 500. Оборачивает весь mux в `httpserver` (включая SCIM и /graphql).
   - `HTTPMetrics(next http.Handler) http.Handler`: гистограмма `http_request_duration_seconds{route,method,status_class}` (route из `r.Pattern`, пустой → "unmatched"; buckets по умолчанию) + счётчик `http_requests_total`.
   - `Handler() http.Handler` — promhttp для registry; монтируется как `GET /internal/metrics`.
2. **Счётчики для capability-пакетов — через интерфейсы у потребителя:**
   - `events.Relay` получает опциональный `RelayMetrics{Published(n int); PublishError(); PendingSet(n int)}`; pending обновляется после каждого drain дешёвым `SELECT count(*) WHERE published_at IS NULL` раз в цикл (частота ≤1/сек — приемлемо).
   - `samlsso` — `SignInMetrics{Success(); Failure(reason string)}` (reason — категория, не данные); `scim` — `OpMetrics{Op(name string)}` по create/update/deactivate/reactivate/delete/list.
   - Кеши `identity.ActiveChecker` и `access.PermissionsCache` — `CacheMetrics{Observe(hit bool)}`; nil-safe (no-op без метрик).
   - Реализации интерфейсов живут в `internal/observability` (prometheus-счётчики), wiring в main.
3. **`SENTRY_DSN`** — опционален во всех окружениях (Sentry-проект может появиться позже production-деплоя; отсутствие DSN — осознанный режим). Дополнительно `SENTRY_SAMPLE_RATE` не вводим (default 1.0 для ошибок).
4. **`/internal/metrics`** — та же внутренняя зона, что и validate: публичная маршрутизация его не открывает (контракт ingress); отдельный порт не заводим до реального требования.
5. **Тесты**: Sentry — через `sentry.ClientOptions.Transport` c тестовым транспортом (официальный `sentry.NewHTTPSyncTransport` не нужен — свой мок, собирающий события); паника — httptest на обёрнутом хендлере; метрики — скрейп `/internal/metrics` в существующих интеграционных окружениях (relay-тест дополняется проверкой счётчиков через `testutil.ToFloat64`/скрейп).

## Risks / Trade-offs

- [slog→Sentry мост может дублировать контекст или отправлять шумные ошибки] → В Sentry идут только `Error`-записи; повторяющиеся ошибки (relay при недоступном брокере) Sentry агрегирует по fingerprint; при шуме — понизить конкретные записи до Warn (осознанные правки).
- [Кардинальность route-метки] → Метка — шаблон маршрута (`/scim/v2/Users/{id}`), не путь; набор маршрутов фиксирован и мал.
- [count(*) по outbox раз в секунду] → Частичный индекс делает его дешёвым; при росте — переключить на приближение из статистики.

## Migration Plan

Аддитивно: деплой без `SENTRY_DSN` ничего не меняет; Prometheus подключается скрейпом `/internal/metrics`. Rollback — откат образа.

## Open Questions

- DSN и проект в Sentry — завести при подключении к инфраструктуре (не влияет на реализацию).
