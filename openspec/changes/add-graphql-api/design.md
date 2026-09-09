# add-graphql-api — Design

## Context

Работают сессии, ext-auth validate и модель доступа с журналом (архивы add-saml-sso-sessions, add-access-control). GraphQL-стек зафиксирован исследованием 2026-09-09: gqlgen + плагин Federation v2; router-кандидат — Cosmo, но его развёртывание вне скоупа. Формат доверенного контекста router→сабграф архитектурно не выбран, поэтому аутентификация — по cookie напрямую. Мотивация — proposal.md; требования — specs/graphql-api/spec.md.

## Goals / Non-Goals

**Goals**
- Рабочий сабграф с авторизацией на сервере и федеративным `User`.
- Актор журнала — реальный пользователь; CLI остаётся для bootstrap.
- Тонкие резолверы: вся логика в capability-пакетах.

**Non-Goals**
- Subscriptions, dataloader-оптимизации, персистентные запросы, rate limiting — по мере надобности.
- Управление составом ролей и создание permissions через API (остаётся seed-файл).
- Интеграция с Cosmo Router и передача доверенного контекста (после выбора формата).

## Decisions

1. **gqlgen v0.17.x, schema-first**: схема в `internal/graphql/schema.graphqls`, конфиг `gqlgen.yml`, генерированный код в `internal/graphql/generated` (в git, как принято с gqlgen). Federation v2 включается конфигом (`federation.version: 2`); `User` — `@key(fields: "id")`.
2. **Аутентификация**: `/graphql` монтируется за существующим session-middleware; резолверы получают viewer из контекста через `session.Manager` + `UserSource` (тот же путь, что `/auth/me`). Отдельного механизма для router-трафика не вводим — router пересылает cookie. Неаутентифицированный запрос обрубается до исполнения запроса (middleware кладёт viewer в контекст; корневые резолверы требуют его наличия) с `extensions.code = UNAUTHENTICATED`.
3. **Авторизация мутаций и журнала** — проверка `identity:access.manage` в резолвере через кешированные эффективные права (`PermissionsCache`); `FORBIDDEN` через gqlgen `graphql.AddError` с кодом в extensions. Приложение `identity` (permissions `access.manage`) добавляется в README-пример seed; сами проверки не зависят от seed.
4. **Схема (минимум)**:
   - `type User @key(fields: "id") { id email name active lastSignInAt roles: [RoleAssignment!]! }` — `roles` виден всем аутентифицированным (справочник назначений нужен админке; чувствительным не считается внутри компании).
   - `type Application { name permissions roles }`, `type Role { name application permissions }`, `type AuditEntry { actor action targetUserId details createdAt }`.
   - `Query { me users(search, includeInactive) user(id) applications accessAuditLog(limit) }`, `Mutation { grantRole(userId, role) revokeRole(userId, role): User! }` — мутации возвращают целевого пользователя.
5. **Новые методы хранения** в `internal/postgres`: `SearchUsers(search string, includeInactive bool, limit)`, `ListApplications()` (с ролями и permissions одним-двумя запросами), `UserRoles(userID)`, `AuditEntries(limit)`. Интерфейсы объявляются в `internal/graphql` (потребитель); `internal/access.Store` не расширяем без нужды.
6. **Ошибки**: доменные (`ErrRoleNotFound`, `identity.ErrUserNotFound`) маппятся в понятные GraphQL-ошибки с кодами; внутренние ошибки логируются и наружу идут как generic `INTERNAL` (без деталей SQL).
7. **Лимиты**: `users` и `accessAuditLog` имеют серверный максимум (например, 200/100 записей) независимо от запрошенного — против случайной выгрузки всего справочника.
8. **Кодогенерация в тулинге**: mise-таск `generate` (`go run github.com/99designs/gqlgen generate`); генерированный код проверяется в CI обычным build/test.

## Risks / Trade-offs

- [Аутентификация по cookie для API, который позже пойдёт через router] → Router пересылает cookie до внедрения доверенного контекста; переход на подписанный контекст затронет только извлечение viewer (одна middleware-функция).
- [Генерированный код gqlgen в репозитории разбухает] → Стандартная практика gqlgen; diff-шум ограничен каталогом `generated`.
- [`roles` у `User` открыт всем аутентифицированным] → Внутренний продукт; если политика ужесточится — поле закроется той же permission-проверкой без изменения схемы.
- [Federation-совместимость с реальным router не проверена] → Спека требует `_service`/`_entities`; композицию с Rails-сабграфами проверит спайк интеграции router (отдельная работа, отмечена в арх-заметке).

## Migration Plan

Только новый endpoint; схема БД не меняется. Rollback — откат образа. Для использования мутаций нужно один раз: добавить `identity` в seed-файл и выдать `identity/admin` первому администратору через `grant-role` (CLI).

## Open Questions

- Точная форма пагинации справочника (offset vs cursor) — для первой админки достаточно limit+search; зафиксировать при появлении реального UI.
