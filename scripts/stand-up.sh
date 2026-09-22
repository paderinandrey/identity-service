#!/usr/bin/env bash
# Поднимает локальный стенд с нуля: Envoy Gateway, зависимости, сервис.
#
# Порядок шагов не косметический:
#   1. образы собираются локально — кластер OrbStack берёт их из того же
#      демона Docker, поэтому pullPolicy: Never;
#   2. миграции запускаются отдельным шагом, а не hook'ом чарта: hook —
#      pre-install и выполняется до создания обычных ресурсов релиза, а на
#      стенде PostgreSQL создаётся тем же релизом, так что в момент hook'а
#      базы ещё нет. В проде база managed, hook работает; его первую
#      установку доказывает отдельный шаг stand-verify;
#   3. сервис перезапускается последним — метаданные IdP он читает один раз
#      при старте, а Keycloak в dev-режиме генерирует ключи заново на каждый
#      запуск пода.
set -euo pipefail

NS="${NS:-identity-stand}"
RELEASE="${RELEASE:-identity-stand}"
CHART="charts/identity-service"
EG_VERSION="${EG_VERSION:-v1.6.1}"

step() { printf '\n== %s\n' "$1"; }

step "Образы"
docker build -q -t identity-service:dev . >/dev/null
docker build -q -t identity-service-keycloak:latest dev/keycloak >/dev/null
docker build -q -t stub-subgraph:dev dev/stub-subgraph >/dev/null
docker build -q -t stub-consumer:dev dev/stub-consumer >/dev/null
docker build -q -t messenger:dev dev/messenger >/dev/null
echo "identity-service:dev, identity-service-keycloak:latest, stub-subgraph:dev, stub-consumer:dev, messenger:dev собраны"

step "Envoy Gateway"
if ! kubectl get ns envoy-gateway-system >/dev/null 2>&1; then
  helm install eg oci://docker.io/envoyproxy/gateway-helm --version "$EG_VERSION" \
    -n envoy-gateway-system --create-namespace --wait --timeout 5m >/dev/null
fi
kubectl wait --for=condition=Available deploy/envoy-gateway -n envoy-gateway-system --timeout=300s >/dev/null
echo "контроллер готов"

step "Namespace и Gateway"
kubectl create namespace "$NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl apply -f deploy/stand/gateway.yaml >/dev/null
echo "gateway применён"

step "Релиз"
helm upgrade --install "$RELEASE" "$CHART" -f "$CHART/values-local.yaml" -n "$NS" >/dev/null
# Keycloak в dev-режиме хранит realm в эфемерном хранилище и импортирует
# его только при старте пода (существующий realm при импорте пропускается),
# поэтому изменения realm-файла применяются перезапуском.
# `wait --for=condition=Available` не годится: условие остаётся истинным,
# пока жив старый под, и сервис успел бы забрать метаданные со старыми
# ключами. `rollout status` ждёт именно новую ревизию.
kubectl rollout restart "deploy/$RELEASE-identity-service-keycloak" -n "$NS" >/dev/null 2>&1 || true
kubectl rollout status "deploy/$RELEASE-identity-service-keycloak" -n "$NS" --timeout=300s >/dev/null
for dep in postgresql redis rabbitmq keycloak echo stub-subgraph stub-consumer messenger; do
  kubectl wait --for=condition=Available "deploy/$RELEASE-identity-service-$dep" -n "$NS" --timeout=300s >/dev/null
  echo "  $dep готов"
done

step "Миграции"
# Вывод сохраняется целиком: фильтр grep по "OK|successfully" ронял скрипт
# через pipefail, когда мигрировать было нечего ("no migrations to run").
migrate_out=$(kubectl run "migrate-$RANDOM" -n "$NS" --rm -i --restart=Never --quiet \
  --image=identity-service:dev --image-pull-policy=Never \
  --overrides="{\"spec\":{\"containers\":[{\"name\":\"migrate\",\"image\":\"identity-service:dev\",\"imagePullPolicy\":\"Never\",\"args\":[\"migrate\"],\"envFrom\":[{\"configMapRef\":{\"name\":\"$RELEASE-identity-service\"}}]}]}}" 2>&1) \
  || { echo "$migrate_out" | tail -5; echo "FAIL: миграции не применились"; exit 1; }
echo "$migrate_out" | grep -E "goose:" | tail -1

step "Суперграф и router"
# Router — отдельный релиз: он не принадлежит ни одному сабграфу. Сначала
# композиция (ей нужен живой identity-сервис ради SDL), затем релиз.
ROUTER="$RELEASE-graphql-router"
ROUTER="$ROUTER" ./scripts/stand-compose-supergraph.sh
helm upgrade --install "$RELEASE-router" charts/graphql-router -n "$NS" \
  --set fullnameOverride="$ROUTER" >/dev/null
kubectl rollout status "deploy/$ROUTER" -n "$NS" --timeout=300s >/dev/null
echo "router ($ROUTER) обслуживает скомпонованный суперграф"

step "Сервис"
kubectl rollout restart "deploy/$RELEASE-identity-service" -n "$NS" >/dev/null
kubectl rollout status "deploy/$RELEASE-identity-service" -n "$NS" --timeout=300s >/dev/null
GW=$(kubectl get gateway -n "$NS" identity-gateway -o jsonpath='{.status.addresses[0].value}')

step "Пользователь стенда"
# Через SCIM с externalId = id пользователя в Keycloak — ровно так, как это
# делает Okta. Тот же id приходит как persistent NameID при входе; связки
# по email нет, поэтому без этого шага войти нельзя.
./scripts/stand-provision-user.sh >/dev/null
echo "qa@example.com заведён с externalId из Keycloak (пароль в realm: password)"

step "Доступ"
# Приложения и роли — декларативно из deploy/stand/access.yaml; роль
# messenger/member получает stand-qa (его создаёт композиция). qa остаётся
# без ролей: на этом держится проверка пустого заголовка прав.
kubectl create configmap stand-access -n "$NS" --from-file=access.yaml=deploy/stand/access.yaml \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
cli() {
  local args="$1"
  kubectl run "cli-$RANDOM" -n "$NS" --rm -i --restart=Never --quiet \
    --image=identity-service:dev --image-pull-policy=Never \
    --overrides="{\"spec\":{\"volumes\":[{\"name\":\"access\",\"configMap\":{\"name\":\"stand-access\"}}],\"containers\":[{\"name\":\"cli\",\"image\":\"identity-service:dev\",\"imagePullPolicy\":\"Never\",\"args\":$args,\"envFrom\":[{\"configMapRef\":{\"name\":\"$RELEASE-identity-service\"}}],\"volumeMounts\":[{\"name\":\"access\",\"mountPath\":\"/stand\"}]}]}}" 2>&1 | tail -1
}
cli '["seed-access","--file","/stand/access.yaml"]'
cli '["grant-role","--email","stand-qa@example.com","--role","messenger/member"]'

cat <<EOF

Стенд поднят. Адрес Gateway: $GW

Хосты (curl --resolve или /etc/hosts):
  identity.localhost -> $GW
  keycloak.localhost -> $GW   (админка: admin/admin)

Вход:              http://identity.localhost/auth/saml/init
                   qa@example.com / password

Проверка целиком:  ./scripts/stand-verify.sh
Снести:            ./scripts/stand-down.sh
EOF
