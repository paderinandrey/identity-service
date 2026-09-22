# messenger-stand — Design

## Context

Стенд: Envoy Gateway (path-роутинг + ext-auth) → `/graphql` в Cosmo
Router → сабграфы identity (`:8080/graphql`, cookie) и `stub-subgraph`
(`Order`, ссылка на `User`). Router — `dependencies.router` в
`values-local.yaml` чарта identity-service; конфиг и суперграф —
ConfigMap `router-config`, который пишет `stand-compose-supergraph.sh`
(`wgc router compose` со статическим списком из двух сабграфов).
NetworkPolicy пускает на публичный порт identity поды с
`app.kubernetes.io/component: router`; probe-поды стенда носят ту же
метку. `dev/stub-subgraph` — образец Go-сабграфа: gqlgen, `withHeaders`
читает `x-identity-*`. Мотивация — proposal.md.

## Goals / Non-Goals

**Goals:**
- Router — самостоятельный компонент, который любой сабграф только
  регистрирует в композиции.
- Третий сабграф доказывает: композиция трёх схем, `User` из третьего
  сервиса разрешается в identity, права из заголовка реально ограничивают
  мутацию.

**Non-Goals:**
- Schema registry (Cosmo Studio) и композиция в CI — остаётся
  скриптом стенда; `print-sdl` для identity — отдельный пункт бэклога.
- Прод-чарт router со всеми ручками (TLS, метрики, HPA) — минимум для
  стенда, с местом под остальное.
- CSRF в messenger: Origin-проверка мутаций — обязанность каждого
  сабграфа; в стабе не реализуется, документ контекста это оговаривает.
- Персистентность сообщений, подписки, консьюмер событий в messenger
  (`author`/`recipient` разрешаются федерацией, проекция не нужна).

## Decisions

1. **`charts/graphql-router` — отдельный чарт, отдельный релиз.**
   Deployment с образом Cosmo Router, Service `:3002`, readiness
   `/health`, `EXECUTION_CONFIG_FILE_PATH`/`CONFIG_PATH` на volume из
   ConfigMap `router.configMapName` (default `<fullname>-config`).
   ConfigMap чарт не создаёт — его наполняет композиция (иначе чарт
   пришлось бы перерендеривать на каждое изменение схем). Метки
   `app.kubernetes.io/name: graphql-router`. Альтернатива — subchart
   внутри identity-service — отвергнута: это ровно та зависимость, от
   которой уходим.

2. **Композиция — список сабграфов в одном месте скрипта.** identity —
   SDL с живого сервиса (как сейчас), `orders` и `messenger` — из
   `schema.graphqls` их модулей. ConfigMap
   `identity-stand-graphql-router-config` в namespace стенда;
   перекомпозиция = рестарт релиза router. Порядок в `stand-up`: релиз
   identity → компоновка → релиз router → рестарт identity-сервиса (как
   было).

3. **`dev/messenger`** — по образцу `dev/stub-subgraph`: свой модуль,
   gqlgen federation v2, distroless-образ. Схема: `Message @key(id)`
   {`id`, `text`, `author: User!`, `recipient: User!`, `sentAt`};
   `User @key(fields: "id", resolvable: false)`; `Query.inbox` — сообщения
   текущему пользователю; `Mutation.sendMessage(recipientId, text)`.
   Контекст: `x-identity-user-id` → текущий пользователь (без него —
   `UNAUTHENTICATED`), `x-identity-permissions` → набор; `sendMessage`
   требует `messenger:messages.send`, иначе `FORBIDDEN`; `inbox` требует
   только аутентификацию. Хранилище — slice под mutex. Правила
   авторизации — чистые функции (`auth.go`) с юнит-тестами; резолверы
   тонкие.

4. **Доступ на стенде декларативно**: `deploy/stand/access.yaml`
   (`identity/admin`, `gsh/observer|operator`, `messenger/member` с
   `messages.send`, `messages.read`); `stand-up` запускает `seed-access` и
   `grant-role --email stand-qa@example.com --role messenger/member`
   через `kubectl run` образа сервиса (как миграции). `stand-qa` уже
   создаёт скрипт композиции. qa без ролей — не трогаем.

5. **Шаг `stand-verify`** после федерации: cookie `stand-qa` через
   `/internal/e2e/login` из probe-пода с меткой tools (внутренний порт;
   как в композиции), затем через gateway: `sendMessage(recipientId:
   <qa id>)` → `author { email }` = `stand-qa@example.com` (разрешено в
   identity через третий сабграф); тот же запрос с cookie qa →
   `FORBIDDEN`, сообщений не прибавилось; `inbox` qa → одно сообщение с
   `author.email` = `stand-qa`. Анонимный запрос к messenger отдельно не
   проверяем: до router аноним не доходит (шаг 11).

6. **NetworkPolicy** identity: peer публичного порта — поды с
   `app.kubernetes.io/name: graphql-router` (метка нового чарта);
   messenger к identity не ходит. Probe-поды стенда получают ту же
   метку, что и раньше по смыслу («я router»).

## Risks / Trade-offs

- [Композиция трёх схем упадёт на `@link`/`@key`] → стаб уже компонуется
  с `resolvable: false`; messenger повторяет ту же декларацию.
- [Права для messenger-мутации приходят из validate и кешируются 60 с] →
  выдача роли в `stand-up` происходит до проверки; шаг verify не зависит
  от кеша.
- [Два релиза в одном namespace усложняют `stand-down`] → `stand-down`
  удаляет оба релиза.

## По итогам ревью Codex (PR #16)

- **P2, доверие к заголовкам без сетевой изоляции.** Messenger верит
  `x-identity-*`, но без NetworkPolicy любой под namespace мог отправить
  ему подделанный контекст напрямую. Шаблон зависимостей стенда получил
  опциональную NetworkPolicy (`ingressFrom`), messenger пускает только
  поды с меткой `app.kubernetes.io/name: graphql-router`; шаг стенда
  проверяет отказ чужому поду и доступ router. Это правило для любого
  сабграфа, который доверяет контексту: записано в README.

