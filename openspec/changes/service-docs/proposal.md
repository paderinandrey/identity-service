# service-docs

## Why

README вырос до 580 строк и совмещает всё: карточку сервиса, онбординг,
справочник переменных и эндпоинтов, интеграции с Okta и RabbitMQ,
устройство relay, чарт и заметки о стенде. Новому человеку негде начать,
а таблица переменных в README уже однажды разошлась с кодом — её
приходится править вручную рядом с тегами `config.Config`.

## What Changes

- README становится карточкой сервиса: что он владеет, статус, стек,
  быстрый старт и таблица ссылок на `docs/`.
- Содержимое README переезжает в `docs/` по видам документов (Diátaxis):
  `GETTING_STARTED`, `DEVELOPMENT`, `CONFIGURATION`, `ARCHITECTURE`,
  `API`, `INTEGRATIONS`, `DEPLOYMENT`, `VOCABULARY`, `playbooks/`
  (стенд, настройка доступа, e2e для фронтендов) и `INDEX`. Переносится
  дословно, затем правится только навигация; существующие `docs/events`
  и `docs/schema` остаются на месте и попадают в индекс.
- `identity-service env --markdown` печатает те же переменные, что и
  `env`, в виде `docs/CONFIGURATION.md`. `mise run docs:config`
  перегенерирует файл, `mise run docs:check` и CI падают, если файл
  устарел или в Markdown есть битая относительная ссылка.

Не меняется: поведение сервиса, имена и значения переменных, вывод `env`
без флага, чарт.

## Capabilities

### Modified Capabilities

- `service-runtime`: команда `env` получает режим `--markdown`.

## Impact

`internal/config`, `cmd/identity-service`, `mise.toml`, CI, `README.md`,
новые файлы в `docs/`, `scripts/check_links.py`.
