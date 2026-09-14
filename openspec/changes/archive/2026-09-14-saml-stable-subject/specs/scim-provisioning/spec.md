# scim-provisioning — delta

## MODIFIED Requirements

### Requirement: Создание пользователя
`POST /scim/v2/Users` SHALL создавать пользователя из атрибутов `userName` (email), `displayName` (имя), `active`, `externalId`; `externalId` SHALL сохраняться как внешняя идентичность провайдера `okta` — тот же subject, по которому выполняется SAML-вход. Повторное создание с существующим `userName` SHALL возвращать 409 с SCIM-ошибкой `uniqueness`. Роли при создании MUST NOT назначаться.

#### Scenario: Успешное создание
- **WHEN** Okta создаёт пользователя с userName, displayName и externalId
- **THEN** ответ 201 содержит SCIM-представление с назначенным id, пользователь находится по email, идентичность `okta` с subject = externalId сохранена

#### Scenario: Дубликат userName
- **WHEN** создаётся пользователь с email существующего (в любом регистре)
- **THEN** ответ 409 со scimType uniqueness, второй пользователь не создаётся
