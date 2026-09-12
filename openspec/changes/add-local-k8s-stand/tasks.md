# add-local-k8s-stand — Tasks

## 1. Зависимости

- [ ] 1.1 `deploy/stand/`: namespace + PostgreSQL, Redis, RabbitMQ (Deployment/Service/PVC-less), готовность по probes
- [ ] 1.2 Keycloak с SCIM-плагином и импортом realm (тот же образ, что в docker-compose); realm подхватывается, SCIM-провайдер настроен на адрес сервиса в кластере

## 2. Сервис и вход

- [ ] 2.1 Установка Envoy Gateway (helm, отдельный namespace) + GatewayClass/Gateway; Gateway получает адрес
- [ ] 2.2 Развернуть identity-service чартом из репозитория с values стенда; Job миграций отрабатывает, `/readyz` зелёный
- [ ] 2.3 Hostname'ы: один адрес для браузера и кластера (CoreDNS-rewrite либо hostAliases); из пода резолвится метадата Keycloak, из браузера — тот же хост
- [ ] 2.4 HTTPRoute + SecurityPolicy: `/auth/*` и `/scim/v2/*` открыты, `/graphql` за ext-auth, `/internal/*` не публикуется

## 3. Проверка и эргономика

- [ ] 3.1 Сквозной скрипт проверки: без cookie `/graphql` → 401 от proxy (до сервиса не доходит); вход через форму Keycloak → cookie → `/graphql` проходит, upstream видит `X-Identity-User-Id/Email/Permissions`
- [ ] 3.2 SCIM-провижининг на стенде: пользователь, созданный в Keycloak, появляется в сервисе и может войти
- [ ] 3.3 `mise run stand:up` / `stand:down`; поднятие с нуля на чистом кластере проходит без ручных шагов
- [ ] 3.4 README: как поднять стенд, какие адреса, чем он отличается от docker-compose
