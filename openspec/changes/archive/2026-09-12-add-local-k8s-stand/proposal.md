# add-local-k8s-stand

## Why

Схема аутентификации не замкнута: `/internal/session/validate` готов, но его никто не вызывает — входного proxy нет ни на стенде, ни в проде. Пока контур не собран целиком, мы не знаем, работает ли ext-auth с нашими заголовками, и команды фронтов не могут ни на что смотреть. Локальный k8s уже есть (OrbStack, активный контекст), Envoy Gateway выбран как реализация.

## What Changes

- Стенд в локальном кластере (namespace `identity-stand`): зависимости (PostgreSQL, Redis, RabbitMQ, Keycloak с нашим realm), identity-service из чарта репозитория, Envoy Gateway с Gateway/HTTPRoute/SecurityPolicy.
- Единые hostname'ы, резолвящиеся одинаково из браузера и изнутри кластера — SAML-редиректы содержат абсолютные URL, а метаданные IdP сервис тянет из пода.
- Публичные маршруты `/auth/*` и `/scim/v2/*` идут в сервис напрямую; `/graphql` — только через ext-auth, который зовёт наш validate и прокидывает `X-Identity-*` в upstream.
- Подъём/снос одной командой (`mise run stand:up` / `stand:down`), плюс скрипт сквозной проверки: вход через Keycloak в браузере → cookie → запрос к защищённому пути → upstream видит заголовки контекста; без cookie — 401 от proxy, до сервиса запрос не доходит.
- Секреты стенда — заведомо dev-значения в манифестах стенда, отдельно от production-values.

Не входит: Cosmo Router и сабграфы (следующий change), EKS, managed-инстансы, TLS.

## Capabilities

Нет изменений поведения сервиса (`skip_specs: true`).

## Impact

Каталог `deploy/stand/` (манифесты зависимостей и Gateway API), mise-таски, README. Кода сервиса не касается; чарт берётся из `add-helm-chart`.
