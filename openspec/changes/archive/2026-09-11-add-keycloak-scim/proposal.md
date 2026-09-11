# add-keycloak-scim

## Why

Локальный Keycloak даёт вход по паролю, но пользователей приходится заводить у нас вручную (CLI/curl). В проде их провижинит Okta по SCIM. Плагин mitodl/keycloak-scim (Apache-2.0, активен) делает Keycloak SCIM-клиентом — локальный контур становится полным зеркалом прода: пользователь заводится в Keycloak и сам прилетает к нам через SCIM.

## What Changes

- `dev/keycloak/Dockerfile`: образ Keycloak 26.7 + jar плагина keycloak-scim; compose собирает его для sso-профиля.
- Realm-импорт: event listener `scim` + федерационный SCIM-провайдер (endpoint `http://host.docker.internal:8080/scim/v2`, BEARER с фиксированным dev-токеном, только user-propagation — Groups наш сервер не поддерживает).
- README: обновлённый сценарий локального SSO (сервис запускается с `SCIM_TOKEN`, пользователи заводятся в Keycloak).
- Тулинг (`skip_specs`).

## Capabilities

Нет изменений (skip_specs).

## Impact

dev/keycloak/, docker-compose.yml, README. Риск: jar собран против более старого Keycloak — проверяется живым запуском; при несовместимости фиксируем и откатываем этот кусок.
