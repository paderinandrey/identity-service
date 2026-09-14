# saml-stable-subject — Tasks

## 1. Домен и хранилище

- [x] 1.1 `identity`: один провайдер `ProviderOkta`; `Resolve` — поиск по `(okta, subject)` и проверка active, без fallback по email; `HasIdentity`/`AttachIdentity` убраны из `Store`; тесты `Resolve` переписаны (неизвестный subject при совпадающем email → `ErrUserNotFound`, привязка не создаётся)
- [x] 1.2 `scim`: `externalId` под провайдером `okta` (create, PUT/PATCH, filter `externalId eq`, представление); тесты обновлены
- [x] 1.3 Миграция `00007` (данные): удалить `user_identities` с `provider='okta'`, переименовать `okta-scim` → `okta`; интеграционный тест: legacy-строки мигрируют, пользователи и роли целы

## 2. Одноразовость и subject в SAML

- [x] 2.1 `samlsso.NonceStore` (consumer-owned): `PutRequestID`, `ConsumeRequestID` (атомарно), `MarkAssertionUsed` (SET NX EX); адаптер `internal/redisstore` на go-redis с тестом на настоящем Redis
- [x] 2.2 `handleInit`: `AuthnNameIDFormat: persistent`, сохранение ID AuthnRequest с TTL 10 мин; отказ хранилища → 503
- [x] 2.3 `handleACS`: чтение `InResponseTo` из декодированного ответа, `ConsumeRequestID` до разбора, `ParseResponse(r, []string{id})` с `AllowIDPInitiated` из конфига (default false), `MarkAssertionUsed` с TTL до `NotOnOrAfter`+skew; Redis-отказ → 503
- [x] 2.4 Email из атрибута `email` (если есть) сравнивается с каталогом; расхождение — `warn` с `user_id` без адресов; в БД не пишется
- [x] 2.5 Конфиг `SAML_ALLOW_IDP_INITIATED` (bool, default false), задокументирован в README как открытое решение
- [x] 2.6 Тесты на in-process IdP (NameID = opaque id, атрибут email через обёртку AssertionMaker): повтор ответа с новым cookie jar → 401; параллельная доставка → ровно один 302; ответ без `InResponseTo` → 401 по умолчанию и 302 с флагом; подделанный `InResponseTo` → 401; смена email при том же NameID → 302 и email в каталоге не тронут; неизвестный subject при совпадающем email → 401; Redis лежит на ACS → 503

## 3. Стенд, Okta, документация

- [x] 3.1 Realm Keycloak: атрибут `stableId` в user profile, маппер NameID (persistent) на него и маппер `email` у обоих SAML-клиентов, фиксированный `id`/`stableId` у встроенного пользователя; `stand-provision-user.sh` задаёт `stableId` = id пользователя и заводит его через SCIM с тем же `externalId`; `stand-verify` проверяет, что subject привязки равен id в Keycloak
- [x] 3.2 README: настройка Okta-приложения (Name ID format Persistent, Application username `user.id`, attribute statements `email`/`name`, проверка SCIM profile mapping `externalId` = `user.id`); упоминания `okta-scim` заменены; `openspec/config.yaml` — устаревшие «не выбран» убраны
- [ ] 3.3 `mise run lint`, `mise run test`, `mise run vuln` чистые; `stand:up` с нуля и `stand:verify` зелёные; CI зелёный
