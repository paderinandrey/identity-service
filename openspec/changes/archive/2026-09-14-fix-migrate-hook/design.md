# fix-migrate-hook — Design

## Context

Helm выполняет `pre-install` хуки до создания обычных ресурсов и ждёт их
завершения. Job миграций использует `serviceAccountName` релиза и
`envFrom: configMapRef` на ConfigMap релиза — оба обычные. Секрет
(`existingSecret`) внешний и существует заранее. На стенде хук выключен;
миграции запускает `stand-up.sh` отдельным подом.

## Decisions

1. **Хук-собственные ресурсы, а не «сделать SA/CM хуками».** Отдельные
   `<release>-migrate` ServiceAccount и ConfigMap с `helm.sh/hook:
   pre-install,pre-upgrade`, `hook-weight: "-1"` (Job — `"0"`),
   `hook-delete-policy: before-hook-creation,hook-succeeded` и
   `argocd.argoproj.io/sync-wave: "-2"` (Job — `"-1"`). Превращать
   основные SA/CM в хуки нельзя: они нужны Deployment'у после установки.
   Дублирование данных ConfigMap — цена самодостаточности; оно из одного
   источника (`.Values.config`), расходиться нечему.

2. **ServiceAccount без токена** (`automountServiceAccountToken: false`):
   миграциям API Kubernetes не нужен. Использовать `default` SA
   namespace'а отвергнуто — его могут ограничивать политиками, а свой SA
   удаляется вместе с хуком.

3. **Проверка первой установки — на живом кластере, шагом стенда.** В CI
   кластера нет, а kind с загрузкой образа и PostgreSQL — отдельная
   инфраструктура ради одного сценария. Стенд уже есть: шаг `stand-verify`
   создаёт БД `identity_hookprobe` в PostgreSQL стенда, секрет с
   `DATABASE_URL`, ставит чарт в пустой namespace `hook-probe` с
   `APP_ENV=development` (остальные переменные тогда не обязательны для
   `migrate`) и `--wait`, проверяет `goose_db_version` = последняя, делает
   `helm upgrade` с изменённым `config.LOG_LEVEL` и убеждается, что хук
   отработал повторно (по событиям Job) — и сносит всё за собой.

4. **Хук на стенде остаётся выключенным**, комментарий исправлен:
   зависимости стенда создаются тем же релизом, на pre-install базы нет.
   Это ограничение стенда, а не хука.

## Risks / Trade-offs

- Шаг проверки первой установки удлиняет `stand-verify` примерно на
  минуту (образ уже собран, ждём только Job).
- В ArgoCD Helm-хуки транслируются в PreSync-хуки; порядок внутри волны
  задаёт `sync-wave`, поэтому SA/CM получают `-2`, Job `-1`.
