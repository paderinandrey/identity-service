# add-access-control — Design

## Context

Аутентификация и сессии работают (архив add-saml-sso-sessions): есть `internal/identity`, `internal/session`, `internal/postgres`, goose-миграции и CLI-подкоманды. Архитектурное решение от 2026-09-09 определяет таблицы (`applications`, `roles`, `permissions`, `role_permissions`, `user_roles`), принадлежность ролей приложениям и запрет копий назначений в бизнес-сервисах. Формат доверенного контекста (подписанный токен) ещё не выбран — временно расширяем заголовки validate. Мотивация — proposal.md; требования — specs/*.

## Goals / Non-Goals

**Goals**
- Модель доступа с инвариантами в БД и append-only журналом в одной транзакции с изменением.
- Эффективные права в ext-auth контексте — GSH/DFM получают их без обращения к нам.
- Bootstrap без API: декларативный seed + назначения через CLI.

**Non-Goals**
- GraphQL API управления (следующее изменение, введёт gqlgen).
- События изменений прав в RabbitMQ; перенос назначений из GSH.
- Иерархии ролей, scoped-права (право на подмножество объектов) — доменные проверки остаются в сервисах.

## Decisions

1. **Схема.** `applications(id uuid, name text UNIQUE)`; `roles(id uuid, application_id fk, name, UNIQUE(application_id, name))`; `permissions(id uuid, application_id fk, name, UNIQUE(application_id, name))`; `role_permissions(role_id fk, permission_id fk, PK(role_id, permission_id))`; `user_roles(user_id fk, role_id fk, PK(user_id, role_id), created_at, granted_by text)`. Инвариант «permission того же приложения, что и роль» — составными FK: в `roles` и `permissions` добавляется UNIQUE(id, application_id), а `role_permissions` хранит `application_id` и ссылается составными ключами (role_id, application_id) и (permission_id, application_id). COMMENT ON для всего.
2. **Журнал** `access_audit_log(id, actor, action, target_user_id nullable, role_id nullable, details jsonb, created_at)` — append-only (без UPDATE/DELETE в коде; REVOKE не делаем — однопользовательская роль БД). Пишется тем же `pgx.Tx`, что и изменение: операции домена принимают транзакцию.
3. **Пакет `internal/access`**: типы `Application`, `Role`, `Permission`; интерфейс `access.Store` (объявлен у потребителя, реализация в `internal/postgres`); операции `GrantRole`, `RevokeRole`, `SeedFromConfig`, `EffectivePermissions(userID) []string` (отсортированный `app:permission`). Кеш прав — по образцу `identity.ActiveChecker` (mutex + map + TTL `PERMISSIONS_CACHE_TTL`, default 60s); инвалидация — только по TTL, что удовлетворяет требованию «не позднее задержки отзыва» без шины инвалидации.
4. **Validate-заголовок** `X-Identity-Permissions: gsh:orders.read,dfm:audits.write`. Пустой набор — пустое значение заголовка (заголовок присутствует, чтобы proxy отличал «нет прав» от «нет данных»). `session.UserSource` расширяется методом `Permissions(ctx, userID)`; `internal/session` не импортирует `internal/access` — main соединяет их через интерфейс (сохраняем направление зависимостей capability-пакетов).
5. **Seed-файл — YAML** (`gopkg.in/yaml.v3`): человекочитаемый формат для рук и ревью. Структура: `applications: [{name, permissions: [..], roles: [{name, permissions: [..]}]}]`. Семантика — «привести к файлу»: создаёт недостающее, приводит состав ролей к файлу (добавляет и удаляет `role_permissions`), не трогает `user_roles`, ничего не удаляет из `applications`/`roles`/`permissions` (удаление — осознанная ручная операция). Идемпотентен.
6. **CLI**: `grant-role --email --role app/role`, `revoke-role --email --role app/role`, `seed-access --file access.yaml`. Актор в журнале — `cli`; позже GraphQL-мутации будут писать актора-пользователя.
7. **Кеш прав и деактивация пользователя** независимы: validate уже проверяет активность через `ActiveChecker`; для неактивного пользователя validate отвечает 401 раньше, чем дойдёт до прав, поэтому «пустой набор для неактивного» в спеке обеспечивается порядком проверок.

## Risks / Trade-offs

- [Заголовок прав — временный формат до подписанного токена] → Изменение формата затронет только validate и proxy-конфигурацию; фиксируем формат в спеке web-sessions, чтобы миграция была явной.
- [TTL-кеш без инвалидации: право живёт до 60s после отзыва] → Согласовано с архитектурным решением («допустимая задержка отзыва»); значение конфигурируемо, при необходимости срочного отзыва — рестарт или снижение TTL.
- [Большой заголовок при сотнях permissions] → Формат компактный; входные proxy обычно допускают 8–16 KB заголовков. Отметить лимит в конфигурации proxy при интеграции.
- [Составные FK усложняют схему] → Это цена инварианта «permission из приложения роли» на уровне БД; альтернатива (проверка в коде) нарушала бы принцип инвариантов в PostgreSQL из AGENTS.md.

## Migration Plan

Новые таблицы; данных нет. `identity-service migrate` перед деплоем, затем `seed-access` начальным файлом. Rollback: goose down (назначения и журнал будут потеряны — на этапе bootstrap приемлемо; после ввода в прод откат вниз не считается обратимым без выгрузки журнала).

## Open Questions

- Точный стартовый список приложений и permissions для seed-файла — согласовать с командами GSH/DFM при интеграции; на реализацию не влияет.
