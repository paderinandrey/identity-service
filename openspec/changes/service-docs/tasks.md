# service-docs — Tasks

## 1. Генерация конфигурации

- [ ] 1.1 `internal/config`: `variables()` читает документацию переменных из тегов; `Describe` и новый `DescribeMarkdown` рендерят её; тест на таблицу
- [ ] 1.2 `cmd/identity-service`: `env --markdown`; тест
- [ ] 1.3 `mise.toml`: `docs:config`, `docs:check`; `scripts/check_links.py`; CI-job `docs`

## 2. Документация

- [ ] 2.1 `docs/`: перенос разделов README по видам документов; `VOCABULARY`, `INDEX`; ссылки на `docs/events`, `docs/schema`, `openspec/`
- [ ] 2.2 README — карточка сервиса с таблицей документов
- [ ] 2.3 `AGENTS.md`/`CLAUDE.md`: где живёт документация и что обновлять в PR

## 3. Приёмка

- [ ] 3.1 `gofmt`, `mise run lint`, `go test -race ./...`, `mise run docs:check`; сверка фактов в документах с кодом (команды, маршруты, переменные, пути)
