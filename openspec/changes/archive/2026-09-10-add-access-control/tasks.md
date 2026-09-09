# add-access-control — Tasks

## 1. Схема и хранение

- [x] 1.1 Goose-миграции: `applications`, `roles`, `permissions` (уникальность в приложении, UNIQUE(id, application_id)), `role_permissions` с составными FK, `user_roles`, `access_audit_log`; COMMENT ON; `migrate` применяет, `goose down` откатывает на тестовой БД
- [x] 1.2 Пакет `internal/access`: типы, интерфейс `Store`, операции `GrantRole`/`RevokeRole` (идемпотентные, журнал в той же транзакции); реализация в `internal/postgres`; интеграционные тесты: идемпотентность, журнал при успехе, отсутствие записи журнала при откате транзакции, permission чужого приложения отклоняется составным FK
- [x] 1.3 `EffectivePermissions`: SQL-объединение по ролям пользователя, отсортированный формат `app:permission`; интеграционные тесты: объединение без дубликатов, роли двух приложений, пустой набор без ролей

## 2. Кеш и validate

- [x] 2.1 Кеш прав с TTL `PERMISSIONS_CACHE_TTL` (config + default 60s); unit-тест: попадание в кеш, истечение TTL подхватывает изменение состава роли
- [x] 2.2 Расширить `session.UserSource` методом `Permissions` и validate-хендлер заголовком `X-Identity-Permissions` (включая пустое значение); обновить тесты session: заголовок с правами, пустой заголовок без ролей, 401-сценарии без заголовка прав

## 3. Bootstrap CLI

- [x] 3.1 `seed-access --file`: YAML-парсинг, «привести к файлу» для приложений/permissions/состава ролей без удаления сущностей и назначений; интеграционные тесты: двойной запуск идемпотентен, сужение состава роли сохраняет назначения
- [x] 3.2 `grant-role` / `revoke-role` по email и `app/role`; интеграционный тест: назначение отражается в EffectivePermissions, повторное назначение идемпотентно, обе операции пишут журнал с актором `cli`

## 4. Завершение

- [x] 4.1 Прогнать `mise run lint`, `mise run test`, `mise run vuln`; все чистые
- [x] 4.2 Обновить README (`PERMISSIONS_CACHE_TTL`, заголовок validate, CLI-команды, пример seed-файла) и вычитать на соответствие реализации
