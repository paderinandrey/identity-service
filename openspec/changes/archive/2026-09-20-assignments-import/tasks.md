# assignments-import — Tasks

## 1. Формат файла и валидация

- [x] 1.1 `internal/access`: типы `ImportFile`/`ImportEntry` (YAML) и `Validate()`: ровно один из `id`/`email`, непустые роли, `app/role`-синтаксис, дубликаты пользователей; юнит-тесты на каждый отказ и на валидный файл

## 2. Хранилище

- [x] 2.1 `AccessStore.GrantRoles(ctx, actor, userID, refs, dryRun) (granted int, err)`: одна транзакция на пользователя, `ON CONFLICT DO NOTHING` + журнал на каждую фактически назначенную роль, откат при неизвестной роли, rollback в dry-run; интеграционные тесты: атомарный откат, повтор без новых записей журнала, dry-run без изменений

## 3. CLI

- [x] 3.1 `main.go`: подкоманда `import-assignments --file --dry-run`: чтение и валидация файла до любых записей, резолв пользователя по id/email, `GrantRoles` на запись, отчёт по строкам и итог, код выхода 1 при хотя бы одном отказе; список команд в сообщении об ошибке `unknown command` обновлён
- [x] 3.2 README «Access control»: формат файла, dry-run, порядок cutover (SCIM-провижининг → replay → import-assignments), что импорт только добавляет

## 4. Приёмка

- [x] 4.1 `gofmt`, `mise run lint`, `go test ./...` (интеграционные тесты хранилища на compose-PostgreSQL); прогон команды на стенде: файл из двух пользователей, повтор, dry-run, запись с несуществующей ролью — вывод и код выхода как в спеке; CI зелёный
