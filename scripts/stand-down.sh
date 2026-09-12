#!/usr/bin/env bash
# Сносит локальный стенд. Envoy Gateway остаётся: он ставится один раз на
# кластер и может обслуживать другие стенды.
set -euo pipefail
NS="${NS:-identity-stand}"
RELEASE="${RELEASE:-identity-stand}"

helm uninstall "$RELEASE" -n "$NS" >/dev/null 2>&1 || true
kubectl delete -f deploy/stand/gateway.yaml --ignore-not-found >/dev/null 2>&1 || true
kubectl delete namespace "$NS" --ignore-not-found >/dev/null 2>&1 || true
echo "стенд снесён (Envoy Gateway оставлен: helm uninstall eg -n envoy-gateway-system)"
