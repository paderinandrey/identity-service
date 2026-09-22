# messenger-stand

## Why

Интеграция с GSH ([[2026-09-18-gsh-integration]]) — первый настоящий
сабграф, и до неё стоит проверить маршрутизацию на третьем сервисе:
федерацию из трёх сабграфов со ссылками на `User`, авторизацию по
заголовку прав в самом сервисе и мутацию через cookie через router.
Сейчас это невозможно честно: Cosmo Router живёт внутри чарта
identity-service как зависимость стенда, а суперграф собирается из двух
жёстко прописанных сабграфов. Третий сервис в чарт identity-service
класть нельзя — router не может принадлежать одному из сабграфов.
Решение владельца 2026-09-22: тестовый сервис — маленький Messenger на
Go (мессенджер и так в архитектуре как будущий сервис).

## What Changes

- **Router выносится в отдельный чарт `charts/graphql-router`** и
  отдельный релиз стенда: Deployment, Service, готовность по `/health`;
  суперграф и конфигурация — ConfigMap, который заполняет скрипт
  композиции. Из чарта identity-service зависимость `router` удаляется;
  `protectedBackendRefs`, NetworkPolicy и стенд-скрипты ссылаются на
  новый Service и метку `app.kubernetes.io/name: graphql-router`.
- **Композиция** берёт список сабграфов из одного места в скрипте (SDL
  identity — с живого сервиса, стаб и messenger — из файлов схем) и
  включает третий сабграф.
- **`dev/messenger`** — Go-сабграф (gqlgen, Federation v2): `Message`
  с `author`/`recipient: User`, `inbox` для текущего пользователя,
  `sendMessage`; текущий пользователь и права — из `x-identity-*`,
  `sendMessage` требует `messenger:messages.send`, иначе `FORBIDDEN`;
  хранилище в памяти; юнит-тесты на авторизацию и адресацию; CI-джоб.
- **Доступ на стенде**: `deploy/stand/access.yaml` с приложениями
  `identity`, `gsh`, `messenger`; `stand-up` сидирует его и выдаёт
  `messenger/member` пользователю `stand-qa` (qa остаётся без ролей —
  на этом держится проверка пустого заголовка прав).
- **`stand-verify`**: шаг «третий сабграф»: `stand-qa` (cookie через
  e2e-вход) отправляет сообщение qa через `/graphql` за gateway — ответ
  собран из messenger и identity (`author.email`); qa без роли получает
  `FORBIDDEN`; `inbox` qa содержит сообщение с разрешённым автором.
- README: стенд с тремя сабграфами и отдельным router.

Не меняется: поведение identity-service. Спек-дельт нет
(`skip_specs`): чарт router, стаб и стенд — эксплуатационные артефакты.

## Capabilities

### New Capabilities

Нет.

### Modified Capabilities

Нет — `skip_specs: true`.

## Impact

- Новые: `charts/graphql-router/*`, `dev/messenger/*`,
  `deploy/stand/access.yaml`.
- Изменяются: `charts/identity-service/values-local.yaml`,
  `scripts/stand-up.sh`, `scripts/stand-compose-supergraph.sh`,
  `scripts/stand-verify.sh`, `scripts/ci-helm-checks.sh`,
  `.github/workflows/ci.yml`, README.
- Совместимость: прод-рендер чарта identity-service не меняется
  (`gateway.protectedBackendRefs` и так per-env).
