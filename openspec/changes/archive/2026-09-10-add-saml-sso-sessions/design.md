# add-saml-sso-sessions — Design

## Context

Скелет сервиса готов (см. архив add-service-skeleton). Стек зафиксирован исследованием от 2026-09-09 в архитектурной заметке; SAML обязателен по внешним требованиям. Поведенческий референс — рабочая реализация в GSH (`~/work/gsh-service/packs/identity`): SP-flow на ruby-saml, подписанный RelayState с TTL, cookie-сессии с лимитом, отказ неизвестным пользователям. Мотивация — proposal.md; требования — specs/*.

## Goals / Non-Goals

**Goals**
- Перенести проверенное поведение GSH SAML на Go без ослабления проверок.
- Сессии как отдельная capability: SAML лишь один из будущих способов их создать.
- Заложить структуру lightweight Clean Architecture (AGENTS.md): capability-пакеты, адаптеры, тонкий транспорт.

**Non-Goals**
- SLO (Single Logout), SCIM, деактивация из Okta, роли, GraphQL, outbox — отдельные изменения.
- Мультитенантность и несколько IdP: один Okta-провайдер, конфигурация одна.

## Decisions

1. **Пакеты по capability** (AGENTS.md, Architecture Style):
   - `internal/identity` — домен: `User`, `ExternalIdentity`, интерфейс `identity.Store` (объявлен у потребителя), операции Resolve-identity/attach.
   - `internal/session` — `session.Manager` поверх `alexedwards/scs` v2 + redisstore: создание с ротацией токена, `/auth/me`-данные, лимит сессий, проверка для ext-auth.
   - `internal/samlsso` — адаптер `crewjam/saml`: настройки SP из IdP-метаданных, init/ACS/metadata, RelayState.
   - `internal/postgres` — реализация `identity.Store` на pgx; `internal/httpserver` остаётся тонким и получает хендлеры capability-пакетов; wiring в `cmd/identity-service`.
2. **crewjam/saml без samlsp-middleware.** Middleware samlsp навязывает свою JWT-cookie-сессию; нам нужен собственный session.Manager. Используем низкоуровневый `saml.ServiceProvider`: `MakeAuthenticationRequest` для init, `ParseResponse` для ACS (проверяет подпись, срок, destination, audience, InResponseTo). Требования SHA-256 и лимита размера проверяем настройками/предварительной проверкой тела запроса (лимит 200 KB, как в GSH). AuthnRequest не подписываем (Okta: Signed Requests disabled, как в GSH).
3. **RelayState — HMAC-подписанный токен** (аналог MessageVerifier из GSH): `base64(payload).base64(HMAC-SHA256)`, payload содержит путь и timestamp, TTL 5 минут, секрет из конфигурации. Санитизация пути идентична GSH: только относительный путь, без `//`, схем и переводов строк. Невалидный RelayState не срывает вход — путь `/`.
4. **ID-first, email-fallback сопоставление пользователя.** GSH ищет только по email (NameID). Мы усиливаем: сначала по (provider='okta', subject=NameID/attribute), при отсутствии — однократная привязка по email активного пользователя без okta-идентичности с сохранением mapping. Это компромисс с правилом AGENTS.md «не линковать только по email»: линковка по email происходит один раз при первом входе (email приходит от доверенного IdP в подписанном assertion), дальше работает стабильный subject. Явно фиксируем как принятое решение.
5. **Сессии: scs v2 с redisstore.** Idle-timeout 24h, абсолютный срок 30 дней, cookie `__identity_session`, `SameSite=Lax` (достаточно для top-level redirect от Okta POST — ACS не читает сессию), `Secure` вне development, `Path=/`. Ротация — `RenewToken` при входе. Лимит одновременных сессий — Redis sorted set `user_sessions:<uuid>` (score = время создания): при входе вытесняем старейшие токены сверх лимита (default 100, как в GSH) и удаляем их ключи scs.
6. **Внутренний endpoint** `GET /internal/session/validate`: читает cookie входящего запроса, отвечает 200 + `X-Identity-User-Id`, `X-Identity-Email` либо 401. Отдельный порт не заводим (роутинг ограничит внешний доступ на уровне ingress/gateway); публичный маршрутизатор сервиса его не публикует в OpenAPI/доках. Ошибки Redis → 503, не 200 и не 401 «по умолчанию» — fail-close с различимым статусом.
7. **Проверка активности пользователя при валидации** — запрос в PostgreSQL по user id с кешем в памяти на конфигурируемую задержку отзыва (default 60s): сессии деактивированного пользователя гаснут не позднее этой задержки без нагрузки на БД на каждый запрос.
8. **PostgreSQL: pgx v5 pool + goose v3.** Миграции — SQL-файлы в `db/migrations`, embed в бинарник, запуск командой `identity-service migrate` (init-контейнер в Kubernetes). Схема: `users` (uuid pk DEFAULT gen, citext email UNIQUE, name, active bool, last_sign_in_at, created/updated), `user_identities` (uuid pk, user_id fk ON DELETE RESTRICT, provider text, subject text, UNIQUE(provider, subject), UNIQUE(user_id, provider), created). COMMENT ON для всех таблиц/колонок. citext-расширение включается миграцией.
9. **CLI-подкоманды** в том же бинарнике: `serve` (default), `migrate`, `create-user --email --name` (идемпотентна по email, upsert без изменения active). Это переиспользует конфигурацию и не открывает HTTP-поверхности.
10. **Конфигурация** расширяется: `DATABASE_URL`, `REDIS_URL`, `BASE_URL`, `FRONTEND_BASE_URL`, `SAML_IDP_METADATA_URL`, `RELAY_STATE_SECRET`, `SESSION_IDLE_TIMEOUT`, `SESSION_LIFETIME`, `SESSION_COOKIE_NAME`, `SESSIONS_MAX_CONCURRENT`, `USER_REVOCATION_DELAY`. В development — дефолты под docker-compose (localhost:5433/6380); вне development отсутствие подключений/URL/секретов — ошибка старта.
11. **Readiness** расширяется зависимостями: ping PostgreSQL и Redis с коротким таймаутом и кешем результата (~2s), чтобы probes не создавали нагрузку.
12. **IdP-метаданные** загружаются на старте (fail-fast) и перечитываются фоново с интервалом (raw-интервал 24h, ошибки обновления не валят сервис — работаем на последних валидных, пишем warning). Для локальной разработки без Okta — mock-IdP (`crewjam/saml/samlidp` в тестах; для ручной проверки допустим публичный тестовый IdP или samlidp-утилита).

## Risks / Trade-offs

- [crewjam/saml не развивается с 2025-05] → Проверки destination/audience/подписей покрываем собственными интеграционными тестами с samlidp; `govulncheck` в lint-таске; готовность к форку зафиксирована в заметке.
- [Email-fallback линковка — окно для захвата аккаунта при компрометации email в Okta] → Линковка только для активных пользователей без существующей okta-идентичности; событие линковки логируется (без NameID — по user UUID); задел под аудит-таблицу в будущем изменении.
- [SameSite=Lax и POST ACS: браузер не пришлёт cookie на cross-site POST] → ACS не требует существующей сессии; новая cookie ставится в ответе ACS на top-level навигации — это работает в актуальных браузерах; логаут защищаем Origin-проверкой.
- [Кеш признака активности задерживает отзыв] → Задержка конфигурируема (default 60s) и зафиксирована в спеке как «не позднее задержки отзыва».
- [Лимит сессий через отдельный sorted set может разойтись с ключами scs] → Вытеснение идемпотентно: удаление несуществующего ключа безопасно; TTL записей в set равен абсолютному сроку сессии.

## Migration Plan

Новые таблицы, существующих данных нет — `identity-service migrate` перед первым запуском. Rollback: goose down + откат образа; сессии в Redis самоистекают. Совместимость с rolling deployment не требуется (первая функциональная версия).

## Open Questions

- Какой атрибут Okta использовать как стабильный subject: NameID (email-формат, нестабилен при смене email) или отдельный атрибут `userId` в assertion. До настройки Okta-приложения принимаем NameID; если тенант отдаёт стабильный `userId`-атрибут — переключимся на него до первого продакшн-входа (меняет только чтение атрибута, не схему).
- Точный интервал фонового обновления IdP-метаданных — уточним при настройке Okta (ротация сертификатов).
