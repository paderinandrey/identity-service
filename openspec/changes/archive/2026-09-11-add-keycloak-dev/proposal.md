# add-keycloak-dev

## Why

Без IdP в сервис невозможно войти: единственный вход — SAML, dev-бэкдора нет намеренно. Для локальной разработки и стейджа без Okta нужен настоящий SAML IdP с парольным входом.

## What Changes

- Keycloak 26.7 в docker-compose под профилем `sso` (не тормозит обычный `mise run up`), host-порт 8081, преднастроенный импортируемый realm `identity`: SAML-клиент под наш SP (подписанные assertions/responses, NameID=email, ACS/entityID на localhost:8080) и тестовый пользователь `qa@example.com` / `password`.
- mise-таск `up-sso`; README: как запустить вход через Keycloak локально.
- Тулинг: спеки не меняются (`skip_specs`).

## Capabilities

### New Capabilities

Нет (skip_specs).

### Modified Capabilities

Нет.

## Impact

docker-compose.yml, `dev/keycloak/realm-identity.json`, mise.toml, README.
