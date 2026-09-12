# add-local-k8s-stand — Tasks

## 0. Кластер

- [ ] 0.1 Включить OrbStack Kubernetes (`orbctl config set k8s.enable true` + рестарт — момент выбирает человек, рестарт останавливает все контейнеры); `kubectl get nodes` отвечает

## 1. Зависимости

- [ ] 1.1 Шаблоны зависимостей в чарте за флагами (PostgreSQL, Redis, RabbitMQ) + `values-local.yaml` с dev-кредами по образцу octoconv; `helm template -f values-local.yaml` рендерит их, дефолтные values — нет
- [ ] 1.2 Keycloak с SCIM-плагином и импортом realm (тот же образ, что в docker-compose) тем же способом; realm подхватывается, SCIM-провайдер настроен на адрес сервиса в кластере

## 2. Сервис и вход

- [ ] 2.1 Установка Envoy Gateway (helm, отдельный namespace) + GatewayClass/Gateway; Gateway получает адрес
- [ ] 2.2 Развернуть identity-service чартом из репозитория с values стенда; Job миграций отрабатывает, `/readyz` зелёный
- [ ] 2.3 Hostname'ы: один адрес для браузера и кластера (CoreDNS-rewrite либо hostAliases); из пода резолвится метадата Keycloak, из браузера — тот же хост
- [ ] 2.4 HTTPRoute + SecurityPolicy: `/auth/*` и `/scim/v2/*` открыты, `/graphql` за ext-auth, `/internal/*` не публикуется

## 3. Проверка и эргономика

- [ ] 3.1 Сквозной скрипт проверки: без cookie `/graphql` → 401 от proxy (до сервиса не доходит); вход через форму Keycloak → cookie → `/graphql` проходит, upstream видит `X-Identity-User-Id/Email/Permissions`
- [ ] 3.2 SCIM-провижининг на стенде: пользователь, созданный в Keycloak, появляется в сервисе и может войти
- [ ] 3.3 `mise run stand:up` / `stand:down` поверх `helm install ... -f values-local.yaml --create-namespace` + `kubectl wait`; поднятие с нуля на чистом кластере проходит без ручных шагов
- [ ] 3.4 Вынести helm-проверки в `scripts/ci-helm-checks.sh`, CI зовёт его же (одна копия проверок на CI и локальный прогон)
- [ ] 3.5 README: как поднять стенд, какие адреса, чем он отличается от docker-compose
