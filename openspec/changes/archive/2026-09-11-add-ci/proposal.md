# add-ci

## Why

Все проверки (линт, тесты с интеграционными окружениями, govulncheck, актуальность генерированного gqlgen-кода) сейчас запускаются только локально — регресс в пуше или PR никто не поймает. Репозиторий опубликован на GitHub, пора включить CI.

## What Changes

- GitHub Actions workflow `ci.yml` на push в `main` и pull request'ы:
  - **lint** — golangci-lint той же версии, что пинит mise (2.13.x);
  - **test** — `go test -race ./...` с сервисами PostgreSQL 18, Redis 8.10 и RabbitMQ 4.2 на тех же нестандартных портах (5433/6380/5673), что и docker-compose — интеграционные тесты работают без правок;
  - **vuln** — govulncheck;
  - **generate-check** — `go tool gqlgen generate` + `git diff --exit-code`: генерированный код не расходится со схемой.
- Версия Go — из `go.mod` (`go-version-file`), один источник правды.

Не входит: сборка/публикация Docker-образа, деплой, кеш-оптимизации сверх стандартного кеша setup-go.

## Capabilities

### New Capabilities

Нет — CI не меняет наблюдаемое поведение сервиса (`skip_specs: true`).

### Modified Capabilities

Нет.

## Impact

- Новый файл `.github/workflows/ci.yml`; README — упоминание CI.
- Внешних зависимостей и изменений кода нет.
