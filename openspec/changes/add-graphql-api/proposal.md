# add-graphql-api

## Why

Управление доступом сейчас доступно только через bootstrap-CLI, а профили — только через `/auth/me`. По архитектурному решению identity-service предоставляет «Users & Access GraphQL API» за общим GraphQL Router: единый интерфейс для админки доступа и справочника сотрудников (списки пользователей для назначений задач в GSH/DFM). Это изменение вводит GraphQL-стек (gqlgen, зафиксирован исследованием 2026-09-09) и делает сервис federation-сабграфом.

## What Changes

- GraphQL endpoint `POST /graphql` на gqlgen с поддержкой Apollo Federation v2: тип `User` объявляется entity (`@key(fields: "id")`), чтобы сабграфы GSH/DFM могли ссылаться на пользователей.
- Queries: `me` (текущий пользователь + его эффективные права), `users` (поиск по имени/email, только активные по умолчанию), `user(id)`, `applications` (с ролями и permissions), `accessAuditLog(limit)`.
- Mutations: `grantRole(userId, role)` и `revokeRole(userId, role)` (роль в формате `app/role`); актором в журнале становится вызывающий пользователь.
- Авторизация на сервере: аутентификация через существующую cookie-сессию (router пересылает cookie); просмотр справочника — любой аутентифицированный пользователь; управление назначениями и чтение журнала — permission `identity:access.manage`. Приложение `identity` с этой permission добавляется в пример seed-файла.
- Без сессии или без нужной permission — GraphQL-ошибки с кодами `UNAUTHENTICATED` / `FORBIDDEN`; сами данные не возвращаются.

Не входит: GraphQL subscriptions, мутации профилей (имя/email придут из Okta/SCIM), управление составом ролей через API (пока только seed-файл), развёртывание GraphQL Router.

## Capabilities

### New Capabilities

- `graphql-api`: GraphQL-сабграф профилей и управления доступом — схема, авторизация операций, federation-совместимость.

### Modified Capabilities

Нет: поведение access-control и сессий не меняется — GraphQL добавляет новый интерфейс поверх существующих операций (актор журнала уже параметризован).

## Impact

- Новый пакет `internal/graphql` (схема, резолверы, генерированный код gqlgen); маршрут `/graphql` за session-middleware в `internal/httpserver`/wiring.
- Новая зависимость: `github.com/99designs/gqlgen` v0.17.x (+ инструмент кодогенерации), файл `gqlgen.yml`.
- Расширения `internal/access`: операции с актором-пользователем уже поддерживаются; добавляется чтение справочника (applications/roles/permissions, журнал) и поиск пользователей в `internal/postgres`.
- README: endpoint, примеры запросов, permission `identity:access.manage` в seed-примере.
