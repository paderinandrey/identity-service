# fix-migrate-hook — Tasks

- [x] 1.1 `hook.db.migrate.yaml`: хук-ServiceAccount и хук-ConfigMap (`<release>-migrate`) с аннотациями и весом `-1` / sync-wave `-2`; Job использует их
- [x] 1.2 `ci-helm-checks.sh`: хук-ресурсы рендерятся с `helm.sh/hook` и без них Job не ссылается на обычные SA/CM
- [x] 2.1 `stand-verify`: шаг «первая установка чарта»: пустой namespace + внешняя БД → `helm install --wait` проходит, версия схемы последняя; `helm upgrade` с изменённым config → хук отработал повторно; уборка
- [x] 2.2 Комментарии в `values-local.yaml` и `stand-up.sh` про причину отключения хука исправлены
- [x] 3.1 README (самодостаточный хук, как проверяется); `mise run chart:check`; `stand:verify` зелёный; CI
