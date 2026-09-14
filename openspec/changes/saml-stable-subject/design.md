# saml-stable-subject — Design

## Context

ACS сейчас: `ParseResponse(r, nil)` с `AllowIDPInitiated: true`,
NameID (email) → `identity.Resolve(okta, nameID, nameID)` с одноразовым
fallback по email. SCIM хранит `externalId` под провайдером `okta-scim`.
crewjam/saml v0.5.1 даёт `PersistentNameIDFormat`, `ParseResponse(r,
possibleRequestIDs)` со сверкой `InResponseTo` при выключенном
`AllowIDPInitiated`, и `Assertion.ID`/`Conditions.NotOnOrAfter`.

Что могут IdP:
- **Okta**: Name ID format `Persistent`, Application username = custom
  expression `user.id` (неизменяемый `00u…`); attribute statements
  `email → user.email`, `name → user.displayName`; SCIM `externalId` по
  умолчанию = `user.id` (проверить profile mapping при заведении).
- **Keycloak (стенд)**: `saml_name_id_format: persistent` даёт user UUID;
  SCIM-плагин mitodl уже отправляет тот же UUID как `externalId` —
  подтверждено строкой `okta-scim|eaeb0995-…` в compose-базе. Атрибуты —
  через `saml-user-property-mapper`.

## Decisions

1. **Subject = стабильный id IdP, один провайдер `okta`.** NameID
   persistent и SCIM `externalId` несут одно значение, поэтому отдельный
   провайдер для SCIM теряет смысл. `ProviderOktaSCIM` удаляется.
   Альтернатива — оставить два провайдера и искать по любому — усложняет
   `Resolve` ради компромисса, который больше не нужен.

2. **Fallback по email удаляется целиком.** `Resolve` = найти по
   `(okta, subject)` + проверка active. Неизвестный subject → 401, даже при
   совпадении email; пользователь заводится SCIM'ом заранее. `HasIdentity`
   и `AttachIdentity` уходят из `identity.Store` (узкие интерфейсы;
   `FindActiveByEmail` остаётся для e2e-login и CLI). Это разрешает
   расхождение спеки `saml-sso` с AGENTS.md в пользу AGENTS.md.

3. **Email из assertion не пишется.** Источник email — SCIM. Атрибут
   `email` (если IdP его отдаёт) сравнивается с каталогом; при расхождении
   — `warn` с `user_id`, без значений (правило «не логировать NameID и
   PII»). Это делает отставание SCIM-синка видимым, но не создаёт второго
   писателя поля.

4. **Одноразовость на двух уровнях, оба в Redis (общие для реплик).**
   - *Request ID*: `handleInit` кладёт `saml:req:<id>` с TTL 10 минут.
     ACS до `ParseResponse` читает `InResponseTo` из декодированного
     ответа (дешёвый разбор корневого элемента; подпись всё равно проверит
     crewjam) и **атомарно потребляет** ключ (`GETDEL`). Нет ключа — 401.
     Потом `ParseResponse(r, []string{id})` с `AllowIDPInitiated: false`,
     чтобы библиотека сверила `InResponseTo` и в Response, и в
     SubjectConfirmation. Потребление до разбора: если разбор упал, ID
     сгорел и вход начинается заново — безопаснее, чем окно между разбором
     и потреблением при параллельной доставке на две реплики.
   - *Assertion ID*: после успешного разбора `SET saml:assertion:<id> NX
     EX <NotOnOrAfter − now + skew>`; не первый — 401. Работает и для
     IdP-initiated, и как страховка от повторной доставки.
   - Redis недоступен → 503, не 401 и не пропуск (fail-close, как у
     сессий).
   Хранилище — consumer-owned интерфейс `samlsso.NonceStore` (три
   метода), реализация в новом адаптере `internal/redisstore`. Не в
   `session`: у того другая ответственность.

5. **IdP-initiated вход выключен по умолчанию.** Флаг
   `SAML_ALLOW_IDP_INITIATED` (default `false`). При включении ACS
   принимает ответы без `InResponseTo`, но assertion-кеш действует.
   Решение владельца (паритет с GSH или отказ) не принято — это допущение,
   зафиксированное явно; смена — одна строка конфигурации.

6. **Data-миграция `00007`** (только данные, без схемы): удалить
   `user_identities` с `provider='okta'` (legacy email-subject привязки,
   невалидные в новой модели) и переименовать `okta-scim` → `okta`.
   Необратима для legacy-привязок — и это осознанно: они были артефактом
   fallback'а. Боевых данных нет; dev-окружения пересоздают пользователей
   через SCIM (`stand:up` это уже делает).

7. **Тесты используют in-process IdP crewjam.** NameID → opaque id;
   атрибут email добавляется обёрткой над `DefaultAssertionMaker`.
   Сценарии Codex (`SAMLResponseCannotBeReplayed`,
   `EmailNameIDChangePreservesLogin`) переносятся как есть плюс
   параллельная доставка одного ответа и Redis-отказ на ACS.

## Risks / Trade-offs

- Cookie сессии и Redis-ключи одноразовости — в одном Redis; его отказ
  блокирует вход целиком. Это уже так для сессий; не новый режим отказа.
- Разбор `InResponseTo` до проверки подписи: читается один атрибут
  корневого элемента, значение используется только как ключ Redis.
  Подделанный `InResponseTo` без ключа → 401; с угаданным ключом →
  подпись всё равно не пройдёт.
- Смена NameID-формата для уже заведённого Okta-приложения потребовала бы
  миграции привязок; сейчас приложения нет — делаем до его появления.
