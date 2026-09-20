# bounded-caches — Tasks

- [x] 1.1 Пакет `internal/cache`: `Cache[V]` с LRU-ёмкостью, TTL, singleflight, `SetClock`, `Len`; тесты: вытеснение старейшей, TTL, отрицательные записи, N одновременных промахов → одна загрузка, ошибка не кешируется
- [x] 1.2 `identity.UserCache` и `access.PermissionsCache` поверх `cache.Cache`, прежний API; существующие тесты счётчиков обращений зелёные
- [x] 2.1 `CACHE_MAX_ENTRIES` (positive int, default 10000) с валидацией и тестом; wiring в `main`
- [x] 2.2 Метрики `cache_entries{cache}`, `cache_evictions_total{cache}`; адаптер; README
- [x] 3.1 `mise run lint/test/vuln`; `stand:verify`; CI
