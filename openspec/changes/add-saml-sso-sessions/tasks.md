# add-saml-sso-sessions — Tasks

## 1. Конфигурация и подключения

- [ ] 1.1 Расширить `internal/config`: `DATABASE_URL`, `REDIS_URL`, `BASE_URL`, `FRONTEND_BASE_URL`, `SAML_IDP_METADATA_URL`, `RELAY_STATE_SECRET`, `SESSION_*`, `SESSIONS_MAX_CONCURRENT`, `USER_REVOCATION_DELAY`; дефолты для development (порты 5433/6380), обязательность вне development; unit-тесты на оба режима проходят
- [ ] 1.2 Подключить pgx v5 pool и Redis-клиент в wiring (`cmd/identity-service`), закрытие при shutdown; `mise run up && mise run run` — сервис стартует и логирует успешные подключения
- [ ] 1.3 Расширить readiness: ping PostgreSQL и Redis с таймаутом и кешем (~2s); тест: при остановленном контейнере `/readyz` → 503, `/healthz` → 200
- [ ] 1.4 Ввести CLI-подкоманды `serve` (default) и `migrate`; `identity-service migrate` применяет пустой набор миграций без ошибок

## 2. Хранилище пользователей

- [ ] 2.1 Goose-миграции: citext, `users`, `user_identities` с ограничениями и COMMENT ON; `migrate` применяет и `goose down` откатывает на тестовой БД
- [ ] 2.2 Пакет `internal/identity`: типы `User`, `ExternalIdentity`, интерфейс `Store`; реализация в `internal/postgres` (FindByIdentity, FindActiveByEmail, AttachIdentity, FindByID, UpsertByEmail, TouchLastSignIn); интеграционные тесты со схемой на PostgreSQL из docker-compose: уникальность email в разных регистрах, уникальность (provider,subject), стабильность UUID
- [ ] 2.3 CLI `create-user --email --name`: идемпотентность по email; интеграционный тест двойного запуска

## 3. Сессии

- [ ] 3.1 Пакет `internal/session`: `Manager` на scs v2 + redisstore — создание с `RenewToken`, cookie-атрибуты по спеке, idle/абсолютный сроки из конфигурации; интеграционные тесты с Redis: создание, истечение idle (короткий таймаут), ротация токена
- [ ] 3.2 Лимит одновременных сессий через sorted set `user_sessions:<uuid>` с вытеснением старейших; тест: вход сверх лимита гасит старейшую сессию
- [ ] 3.3 `POST /auth/logout` с проверкой Origin: удаление сессии, сброс cookie; тесты — успешный выход, чужой Origin отклонён, повторное использование старого токена → 401
- [ ] 3.4 `GET /auth/me`: 200 c UUID/email/именем при валидной сессии, 401 без неё; учёт признака активности пользователя с кешем на `USER_REVOCATION_DELAY`; тесты обоих случаев и деактивированного пользователя
- [ ] 3.5 `GET /internal/session/validate`: 200 + `X-Identity-User-Id`/`X-Identity-Email` или 401; при недоступном Redis — 503 (fail-close, тест с отключённым Redis)

## 4. SAML SSO

- [ ] 4.1 Пакет `internal/samlsso`: загрузка и парсинг IdP-метаданных на старте (fail-fast) с фоновым обновлением; тест: невалидные метаданные → ошибка старта
- [ ] 4.2 RelayState: HMAC-SHA256 подпись, TTL 5 минут, санитизация пути как в GSH; unit-тесты: абсолютный URL → `/`, protocol-relative → `/`, просроченный/битый → nil
- [ ] 4.3 `GET /auth/saml/init`: AuthnRequest + redirect на Okta c RelayState; тест: redirect URL содержит SSO-адрес IdP и SAMLRequest
- [ ] 4.4 `POST /auth/saml/acs`: лимит тела 200 KB, `ParseResponse` (подпись, срок, drift 60s, destination, audience), сопоставление identity (subject → email-fallback с сохранением привязки), создание сессии, redirect на `FRONTEND_BASE_URL` + путь; при любой ошибке — 401 без сессии и без логирования assertion
- [ ] 4.5 `GET /auth/saml/metadata`: SP XML с entity ID и ACS URL; тест на content type и содержимое
- [ ] 4.6 Интеграционный тест полного цикла с `crewjam/saml/samlidp` как mock-IdP: успешный вход (сессия + redirect), битая подпись → 401, неизвестный пользователь → отказ, повторный вход по сохранённому subject при изменённом email, деактивированный пользователь → 401

## 5. Завершение

- [ ] 5.1 Прогнать `mise run lint`, `mise run test` (включая интеграционные с docker-compose), `govulncheck ./...`; все чистые
- [ ] 5.2 Обновить README: новые переменные окружения, эндпоинты, порядок запуска (`migrate` → `serve`, `create-user`), заметка о mock-IdP для локальной разработки; вычитать на соответствие реализации
