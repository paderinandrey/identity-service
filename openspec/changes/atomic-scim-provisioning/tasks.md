# atomic-scim-provisioning — Tasks

- [ ] 1.1 `identity.Provision` и `identity.ErrIdentityTaken`; `mapDuplicate` различает constraint'ы
- [ ] 1.2 tx-хелперы `updateProfileTx`/`replaceIdentityTx`/`setActiveTx`; `ProvisionCreate` и `ProvisionApply` (с `ProvisionOutcome`) в одной транзакции; старые методы переведены на хелперы
- [ ] 1.3 Тесты PostgreSQL: create с занятым externalId → `ErrIdentityTaken`, пользователя и события нет; apply с конфликтом → пользователь и outbox нетронуты; apply с профилем + деактивацией → два события в порядке, outcome.Deactivated
- [ ] 2.1 `scim.UserStore` сужен; `handleCreate`/PUT/PATCH/DELETE через `ProvisionCreate`/`ProvisionApply`; отзыв сессий по outcome; 409 с разными сообщениями
- [ ] 2.2 Тесты SCIM (сценарии Codex F5): POST с занятым externalId → 409 и пользователя нет; PATCH имя+конфликт+active=false → 409, имя/version/active нетронуты, событий нет; существующие тесты зелёные
- [ ] 3.1 README (атомарность, сообщения 409); `mise run lint`, `test`, `vuln`; `stand:verify`; CI
