# cleanenv-config — Tasks

## 1. Конфигурация

- [ ] 1.1 `go get github.com/ilyakaznacheev/cleanenv@v1.5.0`; `internal/config/config.go`: теги `env`, `env-default`, `env-description` на `Config`; `Load` через `cleanenv.ReadEnv` + `applyTextDefaults` + `validate` с прежними правилами и текстами; `Describe` через `cleanenv.GetDescription`
- [ ] 1.2 `config_test.go`: хелпер `unsetenv`; `TestDefaultsMatchTags`; `TestEmptyValues` (текст → default, типизированные → ошибка с именем переменной); `TestDescribeListsEveryVariable`; существующие тесты проходят без ослабления

## 2. CLI и документация

- [ ] 2.1 `cmd/identity-service/main.go`: подкоманда `env` до `config.Load`; список команд в сообщении об ошибке
- [ ] 2.2 README: примечание про пустые значения и `identity-service env` после таблицы конфигурации

## 3. Приёмка

- [ ] 3.1 `gofmt`, `mise run lint`, `mise run test`; `go run ./cmd/identity-service env` печатает все переменные; `go run ./cmd/identity-service` стартует на dev-значениях
