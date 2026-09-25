# cleanenv-config — Tasks

## 1. Конфигурация

- [x] 1.1 `go get github.com/ilyakaznacheev/cleanenv@v1.5.0`; `internal/config/config.go`: теги `env`, `env-default`, `env-description` на `Config`; `Load` через `cleanenv.ReadEnv` + `applyTextDefaults` + `validate` с прежними правилами и текстами; `Describe` рендерит те же теги (имя, тип по-человечески, описание, default)
- [x] 1.2 `config_test.go`: хелпер `unsetenv`; `TestDefaultsMatchTags`; `TestEmptyValues` (текст → default, типизированные → ошибка с именем переменной); `TestDescribeListsEveryVariable`; существующие тесты проходят без ослабления

## 2. CLI и документация

- [x] 2.1 `cmd/identity-service/main.go`: подкоманда `env` до `config.Load`; список команд в сообщении об ошибке
- [x] 2.2 README: примечание про пустые значения и `identity-service env` после таблицы конфигурации

## 3. Приёмка

- [x] 3.1 `gofmt`, `mise run lint`, `go test -race ./...` (интеграционные тесты PostgreSQL/Redis пропущены: compose не поднят, конфигурацию они не трогают); `go run ./cmd/identity-service env` печатает все переменные; `migrate` без переменных доходит до подключения к БД на dev-значениях
