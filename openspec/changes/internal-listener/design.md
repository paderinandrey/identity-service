# internal-listener — Design

## Context

Один `http.Server` на `LISTEN_ADDR`; `httpserver.New` собирает mux из
health-маршрутов и `WithRoutes`, оборачивает recover + метриками.
`main.go` вешает `/auth/`, `/internal/` и `/graphql` на один `authMux` за
session-middleware, `/scim/v2/` и `GET /internal/metrics` — рядом. Чарт:
Service с одним портом `http`, SecurityPolicy ext-auth и ServiceMonitor
ходят на него же; NetworkPolicy нет. На стенде Envoy-прокси живёт в
`envoy-gateway-system` (метка `kubernetes.io/metadata.name`), router и
зависимости — в `identity-stand`; `stand-compose-supergraph.sh` берёт
e2e-сессию из ad-hoc `kubectl run`-пода по Service:8080. Проверено
2026-09-18: k3s под OrbStack применяет NetworkPolicy (deny-all блокирует
соединение, без политики — 200). Мотивация — proposal.md.

## Goals / Non-Goals

**Goals:**
- Внутренняя зона недостижима с публичного порта на уровне процесса, а не
  только маршрутизации, и недостижима из «чужих» подов на уровне сети.
- Свойство «контекст upstream'а — только от validate» доказано на
  стенде, включая пустое значение прав.
- Стенд и CI-проверки чарта переведены и проходят.

**Non-Goals:**
- Подписанный контекст / mTLS между proxy и сабграфами — отдельное
  решение (записано в плане как отложенное).
- Egress-политика: адреса managed-зависимостей — свойство окружения.
- Изоляция зависимостей стенда друг от друга (PostgreSQL, Redis и т. п.).
- Двухфазный раскат для уже развёрнутых окружений: сервис нигде не
  развёрнут, см. Migration Plan.

## Decisions

1. **Два `http.Server` в одном `httpserver.Server`, а не два процесса и
   не один сервер с фильтром по адресу.** `New(addr, …, WithInternal(addr,
   register))`: второй mux с собственными маршрутами, тот же wrapper
   (recover + метрики; метки маршрута берутся из `r.Pattern`, поэтому
   обе зоны учитываются без доработок). `Run` поднимает оба, ошибка
   любого — ошибка `Run`; shutdown — параллельно, один таймаут. Фильтр
   по локальному адресу внутри одного сервера отвергнут: сохраняет один
   порт снаружи, а цель — разные порты, чтобы NetworkPolicy и Service
   могли их различать.

2. **`/healthz` и `/readyz` только на публичном адресе.** Probes задаёт
   чарт на порт `http`; дублировать на внутренний нечем оправдать, а
   лишний маршрут во внутренней зоне — лишняя поверхность. Внутренний mux
   получает ровно `/internal/session/validate[/]`, `/internal/e2e/login`
   (если включён) и `/internal/metrics`; всё остальное — 404 из пустого
   mux.

3. **`INTERNAL_LISTEN_ADDR`, default `:8081`; равенство с `LISTEN_ADDR` —
   ошибка конфигурации** с явным сообщением, а не ошибка bind (та была бы
   «address already in use» без объяснения).

4. **Service: порты `http` (8080) и `internal` (8081)**; SecurityPolicy
   `backendRefs.port` и ServiceMonitor `endpoints.port` → `internal`.
   HTTPRoute не трогаем: наружу по-прежнему только публичный порт.
   Deployment объявляет второй containerPort по имени, чтобы
   NetworkPolicy ссылалась на имя порта, а не число.

5. **NetworkPolicy — часть чарта, `networkPolicy.enabled: true` по
   умолчанию, только `Ingress`.** Два правила: порт `internal` — из
   namespace gateway (`networkPolicy.gatewayNamespace`, default
   `envoy-gateway-system`, по `kubernetes.io/metadata.name`), из
   namespace мониторинга (`networkPolicy.monitoringNamespace`, пусто =
   правила нет) и `networkPolicy.internal.extraFrom` (список
   `NetworkPolicyPeer`); порт `http` — из namespace gateway и
   `networkPolicy.public.extraFrom` (сюда per-env кладут router). Включено
   по умолчанию потому, что политика и есть требуемое свойство; на CNI
   без enforcement она инертна и ничего не ломает. Egress не задаём
   (Non-Goals). Selector по namespace-метке, а не по меткам подов Envoy:
   имена подов прокси генерирует Envoy Gateway, метки namespace стабильны.

6. **Стенд.** `values-local.yaml`: `public.extraFrom` — поды с
   `app.kubernetes.io/component: router` того же namespace;
   `internal.extraFrom` — поды с меткой `identity-stand/tools: "true"`.
   `stand-compose-supergraph.sh` запускает curl-под с этой меткой и ходит
   на `:8081` за e2e-сессией и на `:8080` за SDL. `stand-verify`:
   (a) `/internal/session/validate` через Service:8080 из разрешённого
   пода → 404; (b) через Service:8081 из пода без метки → соединение не
   устанавливается (curl exit 28/7), из пода с меткой → 401 без cookie;
   (c) `/debug/echo` с cookie и подделанными `x-identity-user-id` /
   `x-identity-permissions` → echo показывает id сессии и пустые права
   (у qa-пользователя стенда ролей нет — это и есть проверка пустого
   значения). Существующий шаг 2 («/internal не опубликован наружу»)
   остаётся: он про HTTPRoute.

7. **Перезапись заголовков — свойство Envoy ext_authz**
   (`allowed_upstream_headers`: заголовки ответа auth-сервиса
   перезаписывают одноимённые в запросе). Отдельного `RequestHeaderModifier
   remove` на HTTPRoute не добавляем: он применяется на уровне route после
   ext_authz и снял бы и легитимные заголовки. Свойство фиксируется в
   спеке и проверяется стендом, а не считается само собой разумеющимся.

8. **`ci-helm-checks.sh`** проверяет в прод-рендере: NetworkPolicy есть,
   в ней два порта и namespace gateway; SecurityPolicy и ServiceMonitor
   ссылаются на `internal`; `networkPolicy.enabled=false` убирает ресурс.

## Risks / Trade-offs

- [Envoy может не передавать заголовок с пустым значением из ответа
  ext-auth, и подделанный `x-identity-permissions` клиента просочится
  для пользователя без ролей] → шаг (c) стенда ловит это до мержа. Если
  подтвердится — остановиться и решать отдельно (кандидаты: sentinel
  вместо пустого значения — это изменение контракта заголовков; или
  режим ext-auth, который срезает заголовки клиента), а не молча
  ослаблять сценарий.
- [Per-env values забыли добавить router в `public.extraFrom`] →
  federation перестаёт работать сразу и заметно (router не достучится до
  сабграфа); README и комментарий в values называют это явно.
- [Сторонние скрейперы/скрипты ходили на 8080 за метриками] → это
  ломается намеренно; сервис нигде, кроме стенда, не развёрнут.
- [CNI окружения не применяет NetworkPolicy] → политика инертна; свойство
  «недоступен из чужих подов» тогда не выполняется, и это надо знать
  про окружение. Стенд enforcement имеет (проверено), staging —
  проверить тем же шагом.
- [Второй порт в HTTP-метриках: одинаковые паттерны маршрутов зон не
  пересекаются] → пересечений нет по построению (разные префиксы).

## Migration Plan

Сервис не развёрнут нигде, кроме локального стенда, поэтому раскат —
один релиз: чарт с новым Service, SecurityPolicy, ServiceMonitor и
NetworkPolicy плюс образ с двумя listener'ами; стенд пересобирается
`stand:up`. Для уже работающего окружения потребовался бы двухфазный
раскат (сначала образ, слушающий обе зоны на обоих портах, затем
переключение SecurityPolicy) — иначе ext-auth в момент rolling-раската
попадает на старые поды без порта 8081. Записать это в README как
правило для будущих изменений портов. Откат — предыдущий релиз чарта
целиком.
