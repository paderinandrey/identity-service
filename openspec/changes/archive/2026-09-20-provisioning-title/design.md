# provisioning-title — Design

## Context

`users` хранит `name text NOT NULL DEFAULT ''`; профиль меняет
`updateProfileTx(email, name)` с `version + 1` и событием `updated`.
`identity.Provision` — nil-поля «не трогать», `ProvisionApply` берёт
незатронутые значения из заблокированной строки. SCIM отдаёт
`userResource` и принимает `userPayload`; `applyPatchOp` знает пути
`userName`, `displayName`, `externalId`, `active`. Событие —
`events.UserSnapshot`, схема и примеры в `docs/events`, golden-тесты
держат producer в документированном наборе полей, а схема допускает
неизвестные поля для консьюмеров. Референсный консьюмер декодирует снимок
через pointer-поля и требует обязательные поля схемы. Мотивация —
proposal.md.

## Goals / Non-Goals

**Goals:**
- `title` проходит SCIM → БД → событие → проекция без смены
  `schemaVersion`, по правилам совместимости из контракта.
- Незатронутые PATCH-запросы не сбрасывают должность (тот же принцип, что
  для остальных полей).

**Non-Goals:**
- `last_sign_in_at` в событии; `name.givenName/familyName` как отдельные
  поля; поиск по должности в GraphQL.

## Decisions

1. **Колонка `title text NOT NULL DEFAULT ''`** рядом с `name`, миграция
   `00012`, только схема. `ADD COLUMN … DEFAULT` в PostgreSQL 11+ не
   переписывает таблицу; старые реплики колонку не читают — rolling-safe.
   Пустая строка, а не NULL: у `name` та же семантика, и в событии
   поле всегда присутствует.

2. **`Provision.Title *string`, `updateProfileTx(id, email, name,
   title)`.** Профильное изменение остаётся одним UPDATE с одним
   инкрементом версии и одним событием, сколько бы полей ни менялось —
   как сейчас для email+name. `UpsertByEmail` (CLI `create-user`) должность
   не трогает: у CLI нет такого аргумента, и upsert не должен затирать
   присланное Okta.

3. **SCIM `title`** — стандартный атрибут core-схемы User (RFC 7643
   §4.1.1), Okta отправляет его без доп. настройки. `userPayload.Title`,
   `userResource.Title` (`omitempty`, чтобы пустая должность не
   засоряла ответ), PATCH: path `title` и ключ в replace без path,
   `/Schemas` объявляет атрибут. PUT без `title` ставит пустую строку —
   PUT заменяет ресурс целиком, так уже работает `displayName`.

4. **Событие: `user.title` всегда в payload; в схеме — необязательное
   свойство.** Ровно тот сценарий, который документ контракта называет
   совместимым: старый консьюмер со своей копией схемы принимает событие
   (unknown-поля разрешены), новый видит поле. Golden-примеры
   перегенерируются `-update`, `TestProducerSendsOnlyDocumentedFields`
   требует, чтобы поле попало в `properties`. Референсный консьюмер:
   `Title string` в `UserSnapshot` и `Title *string` в `rawUser` без
   проверки на nil — поле необязательное по схеме, и консьюмер обязан
   принимать снимки без него.

5. **GraphQL `User.title: String!`** — пустая строка вместо nullable,
   как у `name`; `gqlgen generate`.

6. **Стенд**: `stand-provision-user.sh` создаёт qa-пользователя с
   `title`; шаг проекции консьюмера меняет через PATCH и имя, и
   должность и проверяет оба поля в проекции — доказательство сквозного
   прохода нового поля.

## Risks / Trade-offs

- [Okta шлёт `title` только если атрибут добавлен в маппинг
  приложения] → runbook Okta (пункт 13) должен это назвать; здесь —
  README.
- [Консьюмер GSH до обновления не знает `title`] → по контракту он его
  игнорирует; `job_title` заполнится после обновления консьюмера и
  `replay-users`.
- [Golden-примеры меняются во всех пяти файлах] → ожидаемо, diff
  показывает ровно одно добавленное поле.

## Migration Plan

Один релиз: миграция добавляет колонку с default, новый код читает и
пишет её. Откат — `DROP COLUMN` в Down; значения теряются, что для
должности приемлемо (источник — Okta, восстанавливается SCIM-синком).
