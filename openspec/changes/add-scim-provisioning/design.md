# add-scim-provisioning — Design

## Context

Референс — SCIM в GSH (Scimitar поверх Rails): маппинг `userName→email`, `displayName→имя`, `externalId`, мягкая деактивация с `sessions.destroy_all`, Bearer-токен со scope. В Go зрелого серверного SCIM-фреймворка уровня Scimitar нет (elimity-com/scim малоактивен), а нужное Okta подмножество невелико — реализуем его стандартной библиотекой. Мотивация — proposal.md; требования — specs/*.

## Goals / Non-Goals

**Goals**
- Ровно то подмножество SCIM 2.0, которое использует Okta-клиент: Users CRUD + PATCH, фильтр eq, discovery.
- Деактивация быстрее задержки отзыва: сессии уничтожаются явно, кеш активности не спасает злоумышленника.
- `externalId` как заготовка стабильной идентичности (`okta-scim`).

**Non-Goals**
- Groups, bulk, sort, сложные фильтры (and/or, co/sw), ETag — Okta для нашего сценария не требует.
- Управление SCIM-токенами в БД (у GSH — APIToken): один токен из конфигурации достаточен до второго клиента.
- Синхронизация ролей из Okta-групп — назначения живут только в этом сервисе.

## Decisions

1. **Свой пакет `internal/scim` без внешних зависимостей.** Подмножество: типы `userResource` (schemas, id, externalId, userName, displayName, active, meta), `listResponse`, `patchOp`, `scimError`. Content-Type `application/scim+json` (принимаем и `application/json`).
2. **Интерфейсы у потребителя** (`internal/scim`):
   - `UserStore`: `FindByID`, `FindByEmailAny` (без фильтра активности), `CreateUser(email, name, active)`, `UpdateUser(id, email, name)`, `SetActive(id, bool)` — новые методы `internal/postgres`.
   - `IdentityStore`: `FindIdentity(provider, subject)`, `AttachIdentity`, `ReplaceIdentity(userID, provider, subject)` — externalId может смениться при пересоздании назначения в Okta.
   - `SessionKiller`: `DestroyAllForUser(userID)` — реализуется в `session.Manager` обходом zset `user_sessions:<uid>` с `DeleteCtx` каждого токена и удалением индекса.
3. **Провайдер идентичности `okta-scim`** (константа в `identity`). SAML-flow не меняется (провайдер `okta`, subject = NameID); связка через общий email. Унификация двух провайдеров в один стабильный subject — отдельное решение после настройки атрибутов Okta (открытый вопрос SAML-design остаётся).
4. **Фильтр** — регэксп `(userName|externalId) eq "..."` без общего парсера; неизвестный фильтр → SCIM 400 `invalidFilter`. Пагинация: `startIndex` (1-based), `count` (cap 200), `totalResults` честный (отдельный COUNT).
5. **PATCH**: только `op: replace` (регистронезависимо); `path` пустой (value-объект с атрибутами) или один из `active`, `userName`, `displayName`, `externalId`; иначе 400 `invalidPath`/`invalidValue`. Применение атомарно: собираем итоговое состояние и пишем одной операцией.
6. **Деактивация** в одном месте (общий helper для PUT/PATCH/DELETE): `SetActive(false)` → `DestroyAllForUser` → лог `scim.user.deactivated`. Ошибка уничтожения сессий не откатывает деактивацию (пользователь уже неактивен; validate добьёт по кешу за `USER_REVOCATION_DELAY`), но логируется как error.
7. **Аутентификация**: `SCIM_TOKEN` (config, опционален во всех окружениях — SCIM подключается, когда настроена Okta; без него маршруты не монтируются). Сравнение `crypto/subtle.ConstantTimeCompare` по SHA-256-дайджестам (выравнивание длины). Токен ≥ 32 символов — валидация конфигурации.
8. **Маршруты** `/scim/v2/...` монтируются вне session-middleware (машинная аутентификация, cookie не участвуют) — отдельный mux в wiring. CSRF неприменим.
9. **Создание**: users.name из `displayName` (fallback: `name.givenName + name.familyName`, иначе часть email до @). Email нормализуется существующим `identity.NormalizeEmail`. 409 через отлов unique violation (pgconn code 23505).
10. **Discovery** — статические JSON-хендлеры: ServiceProviderConfig (patch: true, filter: true c maxResults, bulk/sort/etag: false), ResourceTypes (User), Schemas (core User с нашими атрибутами).

## Risks / Trade-offs

- [Ручная реализация протокола — риск несовместимости с Okta-клиентом] → Подмножество списано с реального поведения Okta (проверка `userName eq` перед созданием, PUT-обновления, PATCH active); интеграционные тесты воспроизводят эти последовательности. Финальная проверка — подключение Okta-приложения на staging (вне скоупа).
- [Один статический токен без ротации] → Ротация = обновление env + рестарт; для одного клиента приемлемо, журнал последнего использования не ведём (у GSH была БД-модель — заведём при необходимости).
- [externalId в отдельном провайдере, SAML продолжает линковаться по email] → Осознанно: смешение субъектов двух каналов опаснее; унификация — после решения по атрибутам Okta.
- [PATCH-подмножество] → Незнакомые операции падают явной SCIM-ошибкой без частичного применения — Okta это переживает и ретраит/сообщает, тихой порчи данных нет.

## Migration Plan

Схема БД не меняется (identities уже универсальны). Включение: задать `SCIM_TOKEN`, настроить Okta SCIM-приложение на `BASE_URL/scim/v2` с этим токеном. Rollback: убрать токен из окружения — маршруты исчезают.

## Open Questions

- Точный состав атрибутов, которые пришлёт настроенное Okta-приложение (title, phoneNumbers и т.п.) — игнорируем лишние поля; расширение маппинга по факту подключения.
