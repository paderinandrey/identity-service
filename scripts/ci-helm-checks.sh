#!/usr/bin/env bash
# Проверки Helm-чарта: lint плюс рендер значимых комбинаций values.
#
# Это ЕДИНСТВЕННАЯ копия этих проверок: джоб `chart` в CI зовёт этот файл,
# и локальный прогон тоже. Две копии проверок — это две проверки, которые
# однажды разойдутся, и локальный «прошло» против устаревшей копии хуже,
# чем отсутствие локального прогона.
#
# Запускать из корня репозитория. Кластер не нужен.
set -euo pipefail

CHART=charts/identity-service

echo "== helm lint"
helm lint "$CHART"

echo "== рендер с дефолтными values (прод-профиль: зависимостей нет)"
out=$(helm template ci "$CHART")
echo "$out" | grep -q "kind: Deployment" || { echo "FAIL: нет Deployment"; exit 1; }
echo "$out" | grep -q "identity-service-migrate" || { echo "FAIL: нет Job миграций"; exit 1; }
echo "$out" | grep -q "kind: HTTPRoute" && { echo "FAIL: маршруты не должны рендериться без gateway.enabled"; exit 1; }
echo "$out" | grep -q "component: postgresql" && { echo "FAIL: зависимости не должны рендериться в проде"; exit 1; }
echo "PASS: Deployment и Job есть, маршрутов и зависимостей нет"

echo "== рендер с gateway, автоскейлом и мониторингом"
out=$(helm template ci "$CHART" \
  --set gateway.enabled=true \
  --set 'gateway.parentRefs[0].name=identity-gateway' \
  --set autoscaling.enabled=true \
  --set serviceMonitor.enabled=true \
  --set existingSecret=identity-secrets)
for kind in HTTPRoute SecurityPolicy HorizontalPodAutoscaler ServiceMonitor; do
  echo "$out" | grep -q "kind: $kind" || { echo "FAIL: нет $kind"; exit 1; }
done
# Контракт ext-auth целиком: без cookie к auth-сервису проверка сессии
# всегда 401, без headersToBackend контекст не доедет до upstream.
echo "$out" | grep -q "headersToExtAuth" || { echo "FAIL: не пробрасывается cookie в ext-auth"; exit 1; }
echo "$out" | grep -q "X-Identity-Permissions" || { echo "FAIL: не пробрасываются заголовки контекста"; exit 1; }
echo "PASS: маршруты, политика ext-auth, HPA и ServiceMonitor на месте"

echo "== рендер оверлея стенда"
out=$(helm template ci "$CHART" -f "$CHART/values-local.yaml")
for component in postgresql redis rabbitmq keycloak echo; do
  echo "$out" | grep -q "component: $component" || { echo "FAIL: в стенде нет $component"; exit 1; }
done
echo "PASS: стенд рендерит все зависимости"
