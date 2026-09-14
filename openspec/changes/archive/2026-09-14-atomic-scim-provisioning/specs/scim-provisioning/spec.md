# scim-provisioning — delta

## MODIFIED Requirements

### Requirement: Создание пользователя
`POST /scim/v2/Users` SHALL создавать пользователя из атрибутов `userName` (email), `displayName` (имя), `active`, `externalId`; `externalId` SHALL сохраняться как внешняя идентичность провайдера `okta` — тот же subject, по которому выполняется SAML-вход. Создание пользователя, привязки и события SHALL выполняться в одной транзакции: ответ 201 MUST гарантировать, что привязка сохранена. Повторное создание с существующим `userName` SHALL возвращать 409 с SCIM-ошибкой `uniqueness`; `externalId`, уже принадлежащий другому пользователю, SHALL возвращать 409 `uniqueness` с сообщением про externalId. При любом конфликте MUST NOT оставаться ни пользователя, ни события. Роли при создании MUST NOT назначаться.

#### Scenario: Успешное создание
- **WHEN** Okta создаёт пользователя с userName, displayName и externalId
- **THEN** ответ 201 содержит SCIM-представление с назначенным id, пользователь находится по email, идентичность `okta` с subject = externalId сохранена

#### Scenario: Дубликат userName
- **WHEN** создаётся пользователь с email существующего (в любом регистре)
- **THEN** ответ 409 со scimType uniqueness, второй пользователь не создаётся

#### Scenario: Занятый externalId
- **WHEN** создаётся пользователь с externalId, уже привязанным к другому пользователю
- **THEN** ответ 409 со scimType uniqueness, пользователь не создан, событие в outbox не записано

### Requirement: Обновление профиля
`PUT /scim/v2/Users/{id}` SHALL заменять профиль (email, имя, active, externalId); `PATCH /scim/v2/Users/{id}` SHALL поддерживать операции `replace` (с path и без) для тех же атрибутов. Все изменения одного запроса — профиль, привязка, признак активности и их события — SHALL применяться в одной транзакции: при ошибке любой части MUST NOT сохраняться ни одна из них и MUST NOT записываться события. Смена email SHALL сохранять UUID пользователя и его назначения. Неизвестные или неподдерживаемые операции PATCH SHALL отклоняться SCIM-ошибкой без частичного применения.

#### Scenario: Смена email через PUT
- **WHEN** PUT приходит с новым userName для существующего пользователя
- **THEN** email обновляется, UUID и назначения ролей сохраняются

#### Scenario: PATCH replace active
- **WHEN** PATCH содержит операцию replace со значением active=false
- **THEN** пользователь деактивируется по правилам требования о деактивации

#### Scenario: Конфликт откатывает весь запрос
- **WHEN** PATCH одновременно меняет имя, задаёт externalId, занятый другим пользователем, и active=false
- **THEN** ответ 409 uniqueness; имя, version, active и привязка пользователя не изменились, новых событий в outbox нет
