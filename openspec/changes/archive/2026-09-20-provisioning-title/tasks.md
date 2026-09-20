# provisioning-title — Tasks

## 1. Модель и хранилище

- [x] 1.1 Миграция `00012_users_title.sql` (`title text NOT NULL DEFAULT ''` + COMMENT); `identity.User.Title`, `identity.Provision.Title`; `userColumns`/`scanUser`, `updateProfileTx` с title, `ProvisionCreate`/`ProvisionApply` учитывают поле; тесты хранилища: create с title, apply меняет только title → version +1 и событие `updated`, PATCH без title не сбрасывает его
- [x] 1.2 `mise run schema:docs` — `docs/schema` перегенерирован

## 2. SCIM

- [x] 2.1 `title` в `userResource` (omitempty), `userPayload`, POST/PUT, PATCH (path `title` и replace без path), `/Schemas`; тесты SCIM: создание с title, PATCH title, PUT без title обнуляет, ответ содержит title

## 3. Событие и консьюмер

- [x] 3.1 `events.UserSnapshot.Title`; `docs/events/user-event.schema.json` — необязательное свойство `user.title`; `docs/events/user-events.md` называет поле и повторяет правило совместимого добавления; примеры перегенерированы `-update`; `go test ./internal/events/` зелёный
- [x] 3.2 `dev/stub-consumer`: `Title` в `UserSnapshot`, необязателен при разборе; тест на снимок без title (принимается) и с title (проецируется)

## 4. GraphQL и стенд

- [x] 4.1 `User.title: String!` в SDL, `gqlgen generate`, `toModelUser`; тест `me`/`user` возвращает title
- [x] 4.2 `stand-provision-user.sh` создаёт qa с title; `stand-verify` в шаге проекции меняет через PATCH имя и должность и проверяет оба в проекции; `mise run stand:up` и `stand:verify` зелёные

## 5. Документация и приёмка

- [x] 5.1 README: `title` в списке SCIM-атрибутов и в примере события; заметка про маппинг атрибута в Okta-приложении
- [x] 5.2 `gofmt`, `mise run lint`, `go test ./...` (интеграционные на compose), `mise run chart:check`; CI зелёный
