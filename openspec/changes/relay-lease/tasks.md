# relay-lease — Tasks

## 1. Схема и хранилище

- [ ] 1.1 Миграция `00009` (схема): `user_id uuid`, `lease_until`, `leased_by`, `quarantined_at`; индексы для выборки eligible-строк и для head-of-line по пользователю; комментарии
- [ ] 1.2 Миграция `00010` (данные): backfill `user_id` из payload существующих строк
- [ ] 1.3 `recordUserEvent` пишет `user_id`; `Store.RequeueQuarantined`; тест миграции backfill

## 2. Relay

- [ ] 2.1 `events.Publisher` (интерфейс) и AMQP-реализация: confirm, `mandatory`, детекция `basic.return` → `ErrUnroutable`
- [ ] 2.2 Цикл: gauges каждый цикл → claim (короткая TX, аренда, head-of-line на пользователя) → publish вне TX → settle (короткая TX: published / backoff / quarantine / unroutable-ожидание)
- [ ] 2.3 Retention раз в час пачками по `OUTBOX_RETENTION`; конфиг с валидацией; `requeue-events` CLI
- [ ] 2.4 Метрики: `outbox_quarantined`, `outbox_oldest_pending_age_seconds`, `outbox_unroutable_total`; адаптер observability; обновление до подключения к брокеру
- [ ] 2.5 Тесты (настоящие PostgreSQL + RabbitMQ): существующие три; два relay → порядок на пользователя; истёкшая аренда подхватывается; отказывающий Publisher → backoff, карантин после N, следующие события пользователя проходят; requeue возвращает; неразмеченное событие ждёт без роста attempts и публикуется после bind; retention удаляет только старое опубликованное; gauges обновляются при мёртвом брокере

## 3. Документация

- [ ] 3.1 README: аренда/порядок/карантин/mandatory/retention, контракт топологии для консьюмеров, новые метрики и CLI; `mise run lint/test/vuln`; `stand:verify`; CI
