# add-graphql-api — Tasks

## 1. Каркас gqlgen

- [ ] 1.1 Добавить gqlgen v0.17.x, `gqlgen.yml` (federation v2, каталог `internal/graphql`), схему `schema.graphqls` с типами из design; `mise run generate` создаёт код, `go build ./...` проходит
- [ ] 1.2 Смонтировать `/graphql` за session-middleware; middleware-извлечение viewer (пользователь + права) в контекст; запрос без сессии → `UNAUTHENTICATED`; тест обоих случаев на `me`

## 2. Справочник

- [ ] 2.1 Методы хранения `SearchUsers` (ILIKE по имени/email, фильтр active, серверный лимит) и `ListApplications` (роли + permissions); интеграционные тесты: поиск без учёта регистра, скрытие неактивных по умолчанию, `includeInactive`
- [ ] 2.2 Резолверы `users`, `user`, `applications`, `User.roles` (метод `UserRoles`); тест: справочник приложений с составом ролей, назначения пользователя видны

## 3. Мутации и журнал

- [ ] 3.1 Резолверы `grantRole`/`revokeRole`: проверка `identity:access.manage`, актор — viewer, маппинг доменных ошибок; тесты: успех с записью журнала (актор = UUID вызывающего), `FORBIDDEN` без права и без изменения данных, неизвестная роль
- [ ] 3.2 Метод `AuditEntries(limit)` + резолвер `accessAuditLog` с той же permission и серверным максимумом; тесты: выдача от новых к старым, `FORBIDDEN` без права

## 4. Federation и завершение

- [ ] 4.1 Federation v2: entity `User`, entity-резолвер по `id`; тесты `_service { sdl }` (содержит `@key`) и `_entities` по существующему и несуществующему id
- [ ] 4.2 Прогнать `mise run lint`, `mise run test`, `mise run vuln`; все чистые
- [ ] 4.3 README: endpoint `/graphql`, примеры запроса/мутации, приложение `identity` с `access.manage` в seed-примере, порядок выдачи первой админской роли; вычитать на соответствие
