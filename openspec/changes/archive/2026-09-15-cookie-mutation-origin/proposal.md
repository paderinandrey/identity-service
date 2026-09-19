# cookie-mutation-origin

## Why

Три мелких расхождения кода с правилами AGENTS.md и спекой, каждое —
одна проверка (обзоры 14.09, Cursor и Codex):

1. **CSRF на GraphQL-мутациях.** AGENTS.md: «Implement CSRF protection
   and validate origins for cookie-authenticated flows». Logout Origin
   проверяет; `grantRole`/`revokeRole` — нет. Транспорт принимает только
   `application/json`, что даёт браузерный барьер через preflight, и
   `SameSite=Lax` не отправит cookie на cross-site POST — поэтому это не
   воспроизведённая атака, а недовыполненное правило. Правило стоит
   выполнить: preflight-барьер зависит от поведения браузера и настройки
   CORS у router'а, а не от нас.
2. **`RELAY_STATE_SECRET` без требований к длине**: в проде обязателен,
   но односимвольный секрет проходит. `SCIM_TOKEN` и `E2E_LOGIN_TOKEN`
   уже требуют 32 символа.
3. **Случайный `development` вне локали**: `APP_ENV` по умолчанию —
   `development`, и в этом режиме `Secure` у cookie снят, а секреты
   подставляются небезопасными значениями. Забытый `APP_ENV` в
   Kubernetes-манифесте — тихая деградация безопасности.

## What Changes

- Мутации GraphQL с cookie-сессией SHALL проходить ту же проверку Origin,
  что logout: Origin отсутствует (не браузер) или входит в список
  доверенных (base URL, frontend URL); иначе `FORBIDDEN` до исполнения.
  Запросы на чтение не проверяются: они не меняют состояния.
- Router на стенде пробрасывает `Origin` в сабграфы; для прода это
  записано как требование к конфигурации router'а — иначе проверка на
  сабграфе не видит браузерного Origin.
- `RELAY_STATE_SECRET` вне development: не короче 32 символов и не равен
  dev-значению по умолчанию.
- Внутри Kubernetes (`KUBERNETES_SERVICE_HOST` задан) `APP_ENV` SHALL быть
  задан явно — старт без него ошибка, а не тихий development.

## Capabilities

### Modified Capabilities

- `graphql-api`: мутации требуют доверенный Origin.
- `service-runtime`: требования к длине секрета RelayState и к явному
  `APP_ENV` в Kubernetes.

## Impact

`internal/graphql` (хук `AroundOperations`), `internal/session` (общий
`OriginSet`), `internal/config`, `cmd`, `scripts/stand-compose-supergraph.sh`,
README, тесты.
