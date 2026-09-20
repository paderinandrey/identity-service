# graphql-limits — Tasks

- [x] 1.1 Схема: `users(search, includeInactive, first: Int! = 50, after: String): UserConnection!`, типы `UserConnection`/`PageInfo`; `gqlgen generate`
- [x] 1.2 Хранилище: `SearchUsers` с keyset `(name, email, id)` и `first + 1`; `AssignmentsForUsers(ids)` одной выборкой; тесты
- [x] 1.3 Резолверы: валидация `first`/`after`, курсор, `UserConnection`; prefetch назначений в request-scoped кеш, `User.roles` из кеша с фолбэком
- [x] 2.1 `NewServer`: `MaxBytesReader` 1 MiB, `FixedComplexityLimit` с множителями для `users`/`accessAuditLog`/`applications`
- [x] 2.2 `httpserver`: `ReadTimeout`, `WriteTimeout`, `IdleTimeout`
- [x] 2.3 Тесты GraphQL: тело > лимита → ошибка без данных; запрос сверх бюджета → ошибка, хранилище не тронуто; пагинация без пересечений и пропусков; `first`/`after` невалидные → `BAD_USER_INPUT`; страница с `roles` — одна выборка назначений (счётчик)
- [x] 3.1 README (лимиты, пагинация, таймауты); `mise run lint/test/vuln`; `stand:verify`; CI
