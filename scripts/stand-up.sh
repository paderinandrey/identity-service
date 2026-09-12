#!/usr/bin/env bash
# Поднимает локальный стенд с нуля: Envoy Gateway, зависимости, сервис.
#
# Порядок шагов не косметический:
#   1. образы собираются локально — кластер OrbStack берёт их из того же
#      демона Docker, поэтому pullPolicy: Never;
#   2. миграции запускаются отдельным шагом, а не hook'ом чарта: на стенде
#      PostgreSQL создаётся тем же релизом, и pre-install hook ждал бы базу,
#      которой Helm ещё не создал (в проде база managed, hook работает);
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
echo "identity-service:dev, identity-service-keycloak:latest собраны"

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
for dep in postgresql redis rabbitmq keycloak echo; do
  kubectl wait --for=condition=Available "deploy/$RELEASE-identity-service-$dep" -n "$NS" --timeout=300s >/dev/null
  echo "  $dep готов"
done

step "Миграции"
kubectl run "migrate-$RANDOM" -n "$NS" --rm -i --restart=Never --quiet \
  --image=identity-service:dev --image-pull-policy=Never \
  --overrides="{\"spec\":{\"containers\":[{\"name\":\"migrate\",\"image\":\"identity-service:dev\",\"imagePullPolicy\":\"Never\",\"args\":[\"migrate\"],\"envFrom\":[{\"configMapRef\":{\"name\":\"$RELEASE-identity-service\"}}]}]}}" \
  2>&1 | grep -E "OK|successfully" | tail -2

step "Сервис"
kubectl rollout restart "deploy/$RELEASE-identity-service" -n "$NS" >/dev/null
kubectl rollout status "deploy/$RELEASE-identity-service" -n "$NS" --timeout=300s >/dev/null
GW=$(kubectl get gateway -n "$NS" identity-gateway -o jsonpath='{.status.addresses[0].value}')

cat <<EOF

Стенд поднят. Адрес Gateway: $GW

Хосты (curl --resolve или /etc/hosts):
  identity.localtest.me -> $GW
  keycloak.localtest.me -> $GW   (админка: admin/admin)

Проверка целиком:  ./scripts/stand-verify.sh
Снести:            ./scripts/stand-down.sh
EOF
