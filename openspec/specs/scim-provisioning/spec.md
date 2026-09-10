# scim-provisioning

## Purpose

Провижининг пользователей из Okta по SCIM 2.0: создание и обновление профилей, своевременная деактивация с отзывом сессий, привязка стабильного внешнего идентификатора. Okta — источник состояния учётных записей; назначения ролей остаются в сервисе.

## Requirements

### Requirement: Аутентификация SCIM-клиента
SCIM-эндпоинты SHALL требовать Bearer-токен, совпадающий с настроенным значением; сравнение SHALL выполняться за константное время. Запрос без токена или с неверным токеном SHALL получать 401 без раскрытия деталей. Если токен не настроен, SCIM-маршруты MUST NOT обслуживаться. Значение токена MUST NOT попадать в логи.

#### Scenario: Неверный токен
- **WHEN** запрос приходит с отсутствующим или неверным Bearer-токеном
- **THEN** ответ 401, тело не содержит данных пользователей

#### Scenario: SCIM выключен
- **WHEN** токен не задан в конфигурации
- **THEN** запросы к SCIM-путям получают 404

### Requirement: Создание пользователя
`POST /scim/v2/Users` SHALL создавать пользователя из атрибутов `userName` (email), `displayName` (имя), `active`, `externalId`; `externalId` SHALL сохраняться как внешняя идентичность провайдера `okta-scim`. Повторное создание с существующим `userName` SHALL возвращать 409 с SCIM-ошибкой `uniqueness`. Роли при создании MUST NOT назначаться.

#### Scenario: Успешное создание
- **WHEN** Okta создаёт пользователя с userName, displayName и externalId
- **THEN** ответ 201 содержит SCIM-представление с назначенным id, пользователь находится по email, идентичность okta-scim сохранена

#### Scenario: Дубликат userName
- **WHEN** создаётся пользователь с email существующего (в любом регистре)
- **THEN** ответ 409 со scimType uniqueness, второй пользователь не создаётся

### Requirement: Чтение и поиск
`GET /scim/v2/Users/{id}` SHALL возвращать пользователя или SCIM-404. `GET /scim/v2/Users` SHALL возвращать ListResponse с `totalResults`, поддерживать фильтры `userName eq "..."` (без учёта регистра) и `externalId eq "..."` и пагинацию `startIndex`/`count`. Деактивированные пользователи SHALL быть видимы (Okta сверяет состояние).

#### Scenario: Поиск по userName перед созданием
- **WHEN** Okta запрашивает `filter=userName eq "USER@example.com"` для существующего в другом регистре email
- **THEN** ListResponse содержит ровно этого пользователя

#### Scenario: Пустой результат
- **WHEN** фильтр не находит пользователя
- **THEN** ответ 200 с totalResults 0 и пустым списком Resources

### Requirement: Обновление профиля
`PUT /scim/v2/Users/{id}` SHALL заменять профиль (email, имя, active, externalId); `PATCH /scim/v2/Users/{id}` SHALL поддерживать операции `replace` (с path и без) для тех же атрибутов. Смена email SHALL сохранять UUID пользователя и его назначения. Неизвестные или неподдерживаемые операции PATCH SHALL отклоняться SCIM-ошибкой без частичного применения.

#### Scenario: Смена email через PUT
- **WHEN** PUT приходит с новым userName для существующего пользователя
- **THEN** email обновляется, UUID и назначения ролей сохраняются

#### Scenario: PATCH replace active
- **WHEN** PATCH содержит операцию replace со значением active=false
- **THEN** пользователь деактивируется по правилам требования о деактивации

### Requirement: Деактивация и реактивация
Установка `active=false` (PUT, PATCH) и `DELETE /scim/v2/Users/{id}` SHALL деактивировать пользователя без физического удаления: признак active снимается, все его сессии SHALL уничтожаться немедленно, вход и проверка сессии SHALL отклоняться. Последующая установка `active=true` SHALL реактивировать пользователя.

#### Scenario: Деактивация уничтожает сессии
- **WHEN** пользователь с действующими сессиями деактивируется через SCIM
- **THEN** его сессии удалены из хранилища, `GET /auth/me` и внутренняя проверка сессии отвечают 401 сразу, без ожидания задержки отзыва

#### Scenario: DELETE — мягкое удаление
- **WHEN** приходит `DELETE /scim/v2/Users/{id}`
- **THEN** ответ 204, пользователь существует с active=false, история и назначения сохранены

#### Scenario: Реактивация
- **WHEN** для деактивированного пользователя приходит active=true
- **THEN** пользователь снова может входить; прежние сессии не восстанавливаются

### Requirement: Discovery
Сервис SHALL отвечать на `GET /scim/v2/ServiceProviderConfig`, `GET /scim/v2/ResourceTypes` и `GET /scim/v2/Schemas` корректными SCIM-документами, отражающими поддерживаемое подмножество (filter, patch; без bulk и sort).

#### Scenario: ServiceProviderConfig
- **WHEN** Okta запрашивает ServiceProviderConfig
- **THEN** ответ описывает поддержку patch и filter и не заявляет незреализованные возможности
