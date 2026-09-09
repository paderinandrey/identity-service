# add-service-skeleton — Tasks

## 1. Основа проекта

- [ ] 1.1 Создать `mise.toml` с пином Go 1.27 (последний патч) и golangci-lint; проверить `mise install && go version`
- [ ] 1.2 Инициализировать Go-модуль (`go mod init`) и структуру `cmd/identity-service`, `internal/config`, `internal/httpserver`; проверить `go build ./...`
- [ ] 1.3 Добавить `.gitignore` для Go-проекта (бинарники, покрытие, `.env`); проверить `git status` — рабочие артефакты не отслеживаются

## 2. Конфигурация и логирование

- [ ] 2.1 Реализовать `internal/config`: чтение `LISTEN_ADDR`, `APP_ENV`, `LOG_LEVEL`, `SHUTDOWN_TIMEOUT` с дефолтами и валидацией; unit-тесты на дефолты и невалидные значения проходят
- [ ] 2.2 Настроить `log/slog` с JSON-хендлером и уровнем из конфигурации; тест: при уровне `info` запись `debug` не попадает в вывод

## 3. HTTP-сервер

- [ ] 3.1 Реализовать `internal/httpserver` со стандартным `http.ServeMux`: `GET /healthz` → 200; тест хендлера проходит
- [ ] 3.2 Добавить `GET /readyz` на `atomic.Bool`: 200 в рабочем состоянии, 503 после начала остановки; тесты обоих состояний проходят
- [ ] 3.3 Реализовать запуск и graceful shutdown в `cmd/identity-service/main.go` (`signal.NotifyContext`, сброс readiness, `server.Shutdown` с таймаутом); проверить вручную: SIGTERM во время `curl` — запрос завершается, процесс выходит с кодом 0
- [ ] 3.4 Интеграционный тест: старт сервера на случайном порту, `/healthz` и `/readyz` отвечают 200, после остановки `/readyz` → 503

## 4. Окружение и инструментарий

- [ ] 4.1 Добавить mise-таски `build`, `test`, `lint`, `run`, `up`; проверить `mise run build && mise run test`
- [ ] 4.2 Настроить `.golangci.yml`; `mise run lint` проходит без ошибок
- [ ] 4.3 Создать `docker-compose.yml` с PostgreSQL 18 и Redis 8.10 (healthcheck-и, volume для данных); `docker compose up -d` — оба контейнера healthy
- [ ] 4.4 Написать multi-stage `Dockerfile` (статический бинарник, `CGO_ENABLED=0`); проверить `docker build` и запуск контейнера: `/healthz` отвечает, SIGTERM останавливает контейнер штатно
- [ ] 4.5 Написать `README.md`: назначение сервиса, ссылка на архитектурное решение, запуск через mise и Docker, список переменных окружения; вычитать на соответствие реализации
