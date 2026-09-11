# add-ci — Tasks

## 1. Workflow

- [x] 1.1 `.github/workflows/ci.yml`: jobs lint / test (с сервисами postgres/redis/rabbitmq на портах 5433/6380/5673) / vuln / generate-check; версия Go из go.mod
- [x] 1.2 Запушить и убедиться, что прогон на GitHub зелёный целиком (включая интеграционные тесты с сервисами)
- [x] 1.3 README: упомянуть CI; вычитать
