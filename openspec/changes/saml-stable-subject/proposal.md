# saml-stable-subject

## Why

Два подтверждённых дефекта SAML-входа (обзор Codex 14.09, сверено по коду в
[[2026-09-14-triage-and-plan]]), оба в одном и том же ACS-коде:

1. **Email работает ключом идентичности.** SP запрашивает
   `EmailAddressNameIDFormat`, и ACS передаёт NameID одновременно как subject и
   как email. Когда в IdP меняется адрес, меняется и NameID: привязка не
   находится, fallback по email отказывает (у пользователя уже есть
   идентичность провайдера) — вход даёт 401. Стабильный `externalId` из SCIM
   хранится отдельным провайдером `okta-scim` и SAML-путём не используется.
   Воспроизведено тестом; противоречит AGENTS.md («email — изменяемый
   атрибут, не ключ») и правилу «не линковать только по совпадению email».
2. **Один подписанный SAMLResponse принимается повторно.** Включён
   `AllowIDPInitiated`, AuthnRequest не отслеживаются, `ParseResponse`
   вызывается без ожидаемых request ID, одноразового учёта assertion нет.
   Повтор того же ответа из другого браузера в пределах `NotOnOrAfter`
   даёт новую сессию. Воспроизведено тестом.

Решение владельца (14.09): ключ идентичности — неизменяемый идентификатор
IdP (Okta `user.id`), email остаётся изменяемым атрибутом с источником в
SCIM; fallback по email убирается, связка идёт только через SCIM
`externalId`. Так делают GitLab, GitHub EMU и AWS IAM Identity Center.

## What Changes

- SP запрашивает `persistent` NameID; subject = стабильный id IdP. Тот же
  id приходит в SCIM `externalId`, поэтому провайдеры `okta` и `okta-scim`
  схлопываются в один `okta`. Email при входе не пишется: его источник —
  SCIM; атрибут `email` из assertion, если есть, используется только для
  предупреждения о рассинхроне.
- Fallback по email при первом входе удаляется: неизвестный subject → 401,
  даже если email совпадает. Пользователь появляется через SCIM до входа.
- SP-initiated вход: ID AuthnRequest сохраняется в Redis и потребляется
  одноразово при ACS; `InResponseTo` обязателен. IdP-initiated вход
  выключен по умолчанию, включается флагом (решение владельца ещё не
  принято — записано как допущение).
- Одноразовое принятие assertion ID в Redis с TTL до `NotOnOrAfter` —
  общее для реплик, действует и при IdP-initiated.
- Data-миграция: legacy-привязки `okta` с email-subject удаляются,
  `okta-scim` переименовывается в `okta`. Боевых пользователей нет.
- Стенд: Keycloak-клиент переводится на persistent NameID и отдаёт `email`
  атрибутом; проверка стенда убеждается, что subject — UUID, а не email.
- README: инструкция по настройке Okta-приложения (NameID, username
  expression, attribute statements, SCIM externalId mapping).

## Capabilities

### Modified Capabilities

- `saml-sso`: инициация входа отслеживает AuthnRequest; ACS требует
  `InResponseTo` (или флаг IdP-initiated), принимает каждый assertion один
  раз, берёт subject из persistent NameID и не связывает по email.
- `users`: один провайдер `okta`; subject — стабильный id IdP, общий для
  SAML и SCIM.
- `scim-provisioning`: `externalId` сохраняется как идентичность
  провайдера `okta`.

## Impact

`internal/samlsso` (SP-конфиг, init, ACS, новый consumer-owned интерфейс
хранилища одноразовых ID), новый адаптер `internal/redisstore`,
`internal/identity` (Resolve без fallback, один провайдер, интерфейс Store
уже), `internal/scim` (константа провайдера), миграция `00007`, конфиг
(`SAML_ALLOW_IDP_INITIATED`), realm Keycloak и `stand-verify`, README.
Ломающее для существующих dev-баз: legacy SAML-привязки удаляются,
пользователей надо пересоздать через SCIM.
