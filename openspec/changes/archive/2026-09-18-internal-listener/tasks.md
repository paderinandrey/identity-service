# internal-listener — Tasks

## 1. Конфигурация и HTTP-сервер

- [x] 1.1 `config`: `INTERNAL_LISTEN_ADDR` (default `:8081`), равенство с `LISTEN_ADDR` — ошибка загрузки с понятным сообщением; тесты на default, переопределение и совпадение адресов
- [x] 1.2 `httpserver`: `WithInternal(addr, register)` — второй `http.Server` с собственным mux и тем же wrapper; `Run` поднимает оба, ошибка любого прерывает запуск, shutdown обоих в один таймаут; тесты: health только на публичном, внутренний маршрут → 404 на публичном и 200 на внутреннем, публичный маршрут → 404 на внутреннем, занятый внутренний адрес → ошибка `Run`, graceful shutdown обоих
- [x] 1.3 `main.go`: validate, e2e-login и metrics регистрируются на внутреннем mux (validate и e2e — за session-middleware), `/internal/` снят с публичного mux; лог старта — оба адреса; `go build`, `go test ./...` зелёные

## 2. Чарт

- [x] 2.1 `values.yaml`: `config.INTERNAL_LISTEN_ADDR`, `service.internalPort`/`internalTargetPort`, блок `networkPolicy` (enabled, gatewayNamespace, monitoringNamespace, internal.extraFrom, public.extraFrom) с комментариями про router
- [x] 2.2 `deployment.yaml` (containerPort `internal`), `service.yaml` (два порта), `securitypolicy.yaml` и `servicemonitor.yaml` → порт `internal`; рендер `helm template` показывает ссылки на `internal`
- [x] 2.3 Новый `networkpolicy.yaml`: только Ingress, два правила по именам портов, namespace-селекторы по `kubernetes.io/metadata.name`, extraFrom-списки; `networkPolicy.enabled=false` не рендерит ресурс
- [x] 2.4 `ci-helm-checks.sh`: прод-рендер содержит NetworkPolicy с обоими портами и namespace gateway, SecurityPolicy/ServiceMonitor ссылаются на `internal`, с `networkPolicy.enabled=false` ресурса нет; `mise run chart:check` зелёный
- [x] 2.5 `values-local.yaml`: `INTERNAL_LISTEN_ADDR`, `public.extraFrom` = router, `internal.extraFrom` = поды с меткой `identity-stand/tools: "true"`

## 3. Стенд

- [x] 3.1 `stand-compose-supergraph.sh`: curl-под с меткой tools, e2e-сессия с `:8081`, SDL с `:8080`; `mise run stand:up` проходит целиком
- [x] 3.2 `stand-verify.sh`: шаги «validate на публичном порту → 404», «validate на внутреннем порту: из пода без метки соединение не устанавливается, из пода с меткой → 401», «подделанные `x-identity-user-id`/`x-identity-permissions` с cookie → echo показывает id сессии и пустые права»; нумерация шагов и итоговая строка обновлены
- [x] 3.3 `mise run stand:verify` зелёный целиком; если шаг с пустыми правами падает — остановиться и зафиксировать в design (риск 1), не ослаблять проверку

## 4. Документация и приёмка

- [x] 4.1 README: таблица переменных (`INTERNAL_LISTEN_ADDR`), раздел про зоны и порты, NetworkPolicy и обязанность добавить router в `public.extraFrom`, правило двухфазного раската при смене портов; docker-run пример с `-p 8081:8081`
- [x] 4.2 `gofmt`, `mise run lint`, `go test ./...`, `mise run chart:check`; итоговый diff просмотрен, CI зелёный
