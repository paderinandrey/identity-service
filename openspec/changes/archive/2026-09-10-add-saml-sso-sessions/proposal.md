# add-saml-sso-sessions

## Why

Сервис пока умеет только отвечать на health-запросы. Его основное назначение — вход через Okta и владение браузерной сессией для всей экосистемы GSH/DFM. SAML продиктован внешними требованиями (решение от 2026-09-09). В GSH уже есть рабочая SAML-реализация на ruby-saml — используем её как поведенческий референс, чтобы не переизобретать проверки и краевые случаи.

## What Changes

- SAML SP-flow по образцу GSH: `GET /auth/saml/init` (redirect на Okta), `POST /auth/saml/acs` (callback), `GET /auth/saml/metadata` (SP-метаданные). Настройки SP строятся из IdP-метаданных по URL; подпись сообщений и assertions обязательна; дополнительные проверки destination и audience; подписанный RelayState с TTL для возврата на исходную страницу без open redirect.
- Серверные сессии в Redis (библиотека scs + redisstore): непрозрачный идентификатор в HttpOnly cookie, idle-timeout и абсолютный срок жизни, ротация идентификатора при входе, `POST /auth/logout`, лимит одновременных сессий пользователя.
- `GET /auth/me` — данные текущего пользователя для frontend.
- Внутренний endpoint проверки сессии для ext-auth входного proxy: валидная сессия → 200 с контекстом пользователя, иначе 401; никогда не fail-open.
- Минимальное хранилище пользователей в PostgreSQL (pgx + goose-миграции): таблицы `users` и `user_identities` (связь provider+subject → пользователь). JIT-создания пользователей нет: неизвестный или неактивный пользователь получает отказ (как в GSH). Провижининг (SCIM) — отдельное будущее изменение; для разработки — CLI-команда создания пользователя.
- Расширение конфигурации: PostgreSQL, Redis, base URL, IdP metadata URL, параметры cookie и сессий, секрет RelayState.

Не входит: SCIM-провижининг, роли/permissions, GraphQL API, outbox/RabbitMQ, Single Logout (SLO), деактивация из Okta.

## Capabilities

### New Capabilities

- `saml-sso`: вход через Okta по SAML — инициация, обработка ACS-callback, SP-метаданные, RelayState, сопоставление identity с пользователем.
- `web-sessions`: серверные cookie-сессии — создание, проверка, завершение, лимиты, внутренний endpoint валидации для ext-auth, `GET /auth/me`.
- `users`: минимальное хранилище пользователей и их внешних идентичностей в PostgreSQL.

### Modified Capabilities

- `service-runtime`: readiness учитывает доступность зависимостей (PostgreSQL, Redis); конфигурация расширяется обязательными параметрами — сервис не стартует без них в production.

## Impact

- Новые пакеты: `internal/identity` (домен пользователей), `internal/session`, `internal/samlsso`, `internal/postgres` (адаптеры), миграции в `db/migrations`.
- Новые зависимости (зафиксированы в исследовании стека от 2026-09-09): `crewjam/saml` v0.5.1, `alexedwards/scs` v2.9.0 + redisstore, `jackc/pgx` v5.11.0, `pressly/goose` v3.28.0, Redis-клиент.
- docker-compose PostgreSQL/Redis начинают реально использоваться приложением; README и конфигурация пополняются.
- Требуется Okta-приложение (SAML) в тенанте и URL его метаданных; для локальной разработки — тестовый IdP.
