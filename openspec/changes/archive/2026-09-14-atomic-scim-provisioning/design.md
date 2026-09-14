# atomic-scim-provisioning — Design

## Context

`handleCreate`: `CreateUser` (своя транзакция, событие `created`), затем
`ReplaceIdentity` (отдельно; ошибка — в лог). `applyState` для PUT/PATCH:
`UpdateUser` → `ReplaceIdentity` → `SetActive`, каждая своя транзакция со
своим событием. `mapDuplicate` сводит любое нарушение уникальности к
`identity.ErrDuplicate`. После `harden-session-revocation` `SetActive`
поднимает поколение сессий и возвращает пользователя; отзыв сессий —
`DestroyAllForUser(userID, epoch)` после транзакции.

## Decisions

1. **Желаемое состояние — один тип, одна операция.** `identity.Provision`
   {Email, Name, Active, ExternalID}. `ProvisionCreate(spec)` и
   `ProvisionApply(id, spec)` в `postgres.Store`; SCIM больше не
   оркестрирует шаги. Пустой `ExternalID` в `Apply` означает «не трогать
   привязку» (PATCH без externalId), как и сейчас.

2. **Одна транзакция, tx-хелперы вместо дублирования.** Существующая
   логика режется на `updateProfileTx`, `replaceIdentityTx`, `setActiveTx`
   (с поколением и событиями) — их зовут и старые методы (остаются для
   тестов и CLI), и новые операции. Порядок внутри `Apply`: `SELECT … FOR
   UPDATE` → профиль → идентичность → активность. Событий может быть два
   (`updated`, затем `deactivated`/`reactivated`) — у каждого свой
   `version`, консьюмеры их и так упорядочивают по версии.

3. **Конфликт различается по constraint'у.** `mapDuplicate` смотрит
   `ConstraintName`: `users_email_unique` → `ErrDuplicate`,
   `user_identities_provider_subject_unique` → новый `ErrIdentityTaken`.
   Обработчик отвечает 409 `uniqueness` в обоих случаях с разным
   сообщением. Альтернатива — предварительный `SELECT` перед вставкой —
   отвергнута: гонка между проверкой и вставкой всё равно требует ловить
   constraint.

4. **Исход операции возвращается явно.** `ProvisionApply` отдаёт
   пользователя и `ProvisionOutcome{Deactivated, Reactivated bool}`, чтобы
   обработчик отозвал сессии (и посчитал метрики) только при реальном
   переходе, а не по сравнению флагов до/после.

5. **DELETE — это Apply с `Active=false`** и остальными полями текущими.
   Один путь деактивации вместо двух.

6. **По итогам ревью Codex (PR #3): присутствие полей явное.** Первая
   версия собирала полный снимок пользователя до транзакции и передавала
   его в `Apply`; параллельная деактивация между чтением и блокировкой
   откатывалась бы PATCH'ем, который `active` не упоминал (lost update).
   Поля `Provision` стали опциональными (`nil` = не упомянуто); значения
   незатронутых полей берутся из строки, заблокированной `FOR UPDATE`,
   внутри транзакции. PATCH заполняет только упомянутое, DELETE — только
   `Active=false`, PUT — всё (замена ресурса).

## Risks / Trade-offs

- Транзакция держит блокировку строки пользователя чуть дольше (три
  UPDATE вместо одного). Провижининг — редкие операции, это ничего не
  стоит.
- Старые методы `CreateUser`/`UpdateUser`/`SetActive`/`ReplaceIdentity`
  остаются в `postgres.Store` ради тестов и CLI, но уходят из
  `scim.UserStore` — интерфейс у потребителя сужается до того, что нужно.
