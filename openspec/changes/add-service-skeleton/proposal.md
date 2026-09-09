# add-service-skeleton

## Why

Identity & Access Service — новый Go-сервис, репозиторий пуст. Чтобы начать работу над SSO, сессиями и управлением доступом, нужен рабочий скелет: собираемое приложение, единая структура каталогов, конфигурация, логирование, health-эндпоинты и локальное окружение. Скелет фиксирует только базовые решения и не предрешает ещё не выбранные части стека (SAML-библиотека, GraphQL, библиотека сессий, инструмент миграций).

## What Changes

- Создаётся Go-модуль и структура каталогов: `cmd/identity-service` (точка входа), `internal/` (код сервиса), версия Go пинится через mise.
- HTTP-сервер на стандартной библиотеке (`net/http`, `log/slog`): эндпоинты `/healthz` (liveness) и `/readyz` (readiness), корректное graceful shutdown по SIGTERM/SIGINT.
- Конфигурация через переменные окружения с валидацией при старте (адрес прослушивания, окружение, уровень логирования).
- Структурированное JSON-логирование через `log/slog`.
- Локальное окружение: `docker-compose.yml` с PostgreSQL и Redis (пока без подключения из приложения — драйверы и библиотеки выбираются отдельным решением).
- Dockerfile (multi-stage) для развёртывания в Kubernetes.
- Инструментарий: mise-таски (build, test, lint, run), golangci-lint, базовый тест на health-эндпоинты.
- `.gitignore`, `README.md` с описанием запуска.

Сознательно НЕ входит в скелет (ждёт отдельных решений): SAML/Okta, сессии в Redis, схема БД и миграции, GraphQL API, outbox/RabbitMQ, endpoint проверки сессии для ext-auth.

## Capabilities

### New Capabilities

- `service-runtime`: базовое поведение процесса сервиса — запуск с конфигурацией из окружения, health/readiness эндпоинты, структурированное логирование, graceful shutdown.

### Modified Capabilities

Нет — репозиторий новый, существующих спецификаций нет.

## Impact

- Новый код: `cmd/identity-service/`, `internal/config/`, `internal/httpserver/`.
- Новые файлы окружения: `go.mod`, `mise.toml`, `docker-compose.yml`, `Dockerfile`, `.golangci.yml`, `.gitignore`, `README.md`.
- Внешние зависимости: только стандартная библиотека Go; инфраструктурные контейнеры PostgreSQL и Redis в docker-compose для будущих изменений.
- Принятые допущения: актуальная стабильная версия Go, пиненная через mise; HTTP-роутер — стандартный `http.ServeMux` (Go ≥ 1.22), сторонний роутер не вводится, пока не выбран GraphQL-стек.
