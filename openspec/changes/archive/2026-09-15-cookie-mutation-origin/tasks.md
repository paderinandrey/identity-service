# cookie-mutation-origin — Tasks

- [x] 1.1 `session.OriginSet`; `NewServer` принимает доверенные origins; хук `AroundOperations` отклоняет мутации с чужим Origin (`FORBIDDEN`) до исполнения; тесты: чужой Origin → FORBIDDEN и журнал пуст, доверенный и пустой → выполняется, query с чужим Origin → выполняется
- [x] 1.2 Router на стенде пробрасывает `origin`; README — требование к router'у в проде
- [x] 2.1 Конфиг: `RELAY_STATE_SECRET` ≥ 32 и ≠ dev-умолчанию вне development; явный `APP_ENV` при `KUBERNETES_SERVICE_HOST`; тесты
- [x] 3.1 README; `mise run lint/test/vuln`; `stand:verify`; CI
