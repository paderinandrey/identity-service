# provisioning-title

## Why

GSH хранит у пользователя `job_title`, который Okta присылает SCIM-атрибутом
`title`, и показывает его в интерфейсе. После переключения SCIM-коннектора
на identity-service ([[2026-09-18-gsh-integration]], стадия A) это поле
исчезнет: сервис принимает только `userName`, `displayName`, `active` и
`externalId`, а событие несёт только id, email, имя, признак активности
и версию. Проекция GSH не сможет заполнить `job_title`, и другого
источника у него не будет. Поле нужно провести насквозь: SCIM → модель →
событие → проекции. Это первое совместимое расширение опубликованного
контракта событий ([[2026-09-19-events-contract]]), и оно же проверяет,
что правила совместимости из документа работают.

## What Changes

- Пользователь получает поле `title` (должность; пустая строка, если не
  задана). Миграция добавляет колонку с default — безопасно для rolling
  deployment.
- SCIM: атрибут `title` в представлении, при `POST`/`PUT`, в `PATCH`
  (`replace` с path `title` и без path) и в `/Schemas`.
- Событие: `user.title` в снимке пользователя. `schemaVersion` остаётся 1
  — поле добавляется как необязательное в схеме, обязательное в payload
  producer'а; примеры и golden-тесты обновляются вместе с ним; документ
  контракта называет поле.
- GraphQL: `User.title: String!`.
- Референсный консьюмер проецирует `title`; стенд проверяет, что
  SCIM-изменение должности доходит до проекции.
- `docs/schema` перегенерируется.

Не меняется: `last_sign_in_at` в событие не добавляется — у GSH это
поле теряет источник, и решение «хранить или убрать» остаётся за GSH.

## Capabilities

### New Capabilities

Нет.

### Modified Capabilities

- `users`: должность в наборе хранимых полей.
- `scim-provisioning`: `title` среди атрибутов создания и обновления.
- `user-events`: `title` в снимке пользователя; совместимое добавление
  поля без смены `schemaVersion`.

## Impact

- `db/migrations/00012_users_title.sql`, `docs/schema`.
- `internal/identity` (`User.Title`, `Provision.Title`), `internal/postgres`
  (колонка, `updateProfileTx`, create/apply), `internal/scim`,
  `internal/events` (payload, golden-тесты, примеры),
  `docs/events/user-event.schema.json`, `docs/events/user-events.md`,
  `internal/graphql` (schema + generated), `dev/stub-consumer`,
  `scripts/stand-provision-user.sh`, `scripts/stand-verify.sh`, README.
- Совместимость: консьюмеры со старой копией схемы принимают событие
  (схема допускает неизвестные поля); GraphQL-клиенты не затронуты.
