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

# Поиск по отрендеренному YAML — подстрокой bash, а не `echo | grep -q`:
# grep -q закрывает пайп на первом совпадении, echo ловит SIGPIPE, и
# pipefail превращает успешную проверку в провал. Ловилось в CI как
# «write error: Broken pipe» на заведомо присутствующем ресурсе.
has() { [[ "$out" == *"$1"* ]]; }

echo "== helm lint"
helm lint "$CHART"

echo "== рендер с дефолтными values (прод-профиль: зависимостей нет)"
out=$(helm template ci "$CHART")
has "kind: Deployment" || { echo "FAIL: нет Deployment"; exit 1; }
has "identity-service-migrate" || { echo "FAIL: нет Job миграций"; exit 1; }
# Хук самодостаточен (F6): свои ServiceAccount и ConfigMap с hook-аннотациями,
# созданные раньше Job по весу; Job не ссылается на ресурсы релиза, которых
# на pre-install ещё нет.
hooked=$(grep -c '"helm.sh/hook": pre-install,pre-upgrade' <<< "$out" || true)
[ "$hooked" -ge 3 ] || { echo "FAIL: хук-ресурсов с аннотацией $hooked, ожидалось 3 (SA, ConfigMap, Job)"; exit 1; }
named=$(grep -c 'name: ci-identity-service-migrate$' <<< "$out" || true)
[ "$named" -ge 3 ] || { echo "FAIL: ресурсов -migrate $named, ожидалось 3"; exit 1; }
grep -A1 'configMapRef:' <<< "$out" | grep -q 'ci-identity-service-migrate' \
  || { echo "FAIL: Job миграций не читает хук-ConfigMap"; exit 1; }
has "serviceAccountName: ci-identity-service-migrate" || { echo "FAIL: Job миграций не использует хук-ServiceAccount"; exit 1; }
has "kind: HTTPRoute" && { echo "FAIL: маршруты не должны рендериться без gateway.enabled"; exit 1; }
has "component: postgresql" && { echo "FAIL: зависимости не должны рендериться в проде"; exit 1; }
# Две зоны: Service и Deployment объявляют порт internal, NetworkPolicy
# ограничивает оба порта и знает namespace прокси.
has "kind: NetworkPolicy" || { echo "FAIL: нет NetworkPolicy"; exit 1; }
has 'kubernetes.io/metadata.name: "envoy-gateway-system"' || { echo "FAIL: NetworkPolicy не ссылается на namespace gateway"; exit 1; }
internal_ports=$(grep -c 'name: internal$' <<< "$out" || true)
[ "$internal_ports" -ge 2 ] || { echo "FAIL: порт internal объявлен $internal_ports раз, ожидалось ≥2 (Service, Deployment)"; exit 1; }
has "- port: internal" || { echo "FAIL: NetworkPolicy не ограничивает порт internal"; exit 1; }
has "- port: http" || { echo "FAIL: NetworkPolicy не ограничивает порт http"; exit 1; }
echo "PASS: Deployment и самодостаточный хук миграций есть, маршрутов и зависимостей нет, NetworkPolicy на обе зоны"

echo "== рендер без NetworkPolicy"
out=$(helm template ci "$CHART" --set networkPolicy.enabled=false)
has "kind: NetworkPolicy" && { echo "FAIL: NetworkPolicy рендерится при networkPolicy.enabled=false"; exit 1; }
echo "PASS: networkPolicy.enabled=false убирает ресурс"

echo "== рендер с gateway, автоскейлом и мониторингом"
out=$(helm template ci "$CHART" \
  --set gateway.enabled=true \
  --set 'gateway.parentRefs[0].name=identity-gateway' \
  --set autoscaling.enabled=true \
  --set serviceMonitor.enabled=true \
  --set existingSecret=identity-secrets)
for kind in HTTPRoute SecurityPolicy HorizontalPodAutoscaler ServiceMonitor; do
  has "kind: $kind" || { echo "FAIL: нет $kind"; exit 1; }
done
# Контракт ext-auth целиком: без cookie к auth-сервису проверка сессии
# всегда 401, без headersToBackend контекст не доедет до upstream.
has "headersToExtAuth" || { echo "FAIL: не пробрасывается cookie в ext-auth"; exit 1; }
has "X-Identity-Permissions" || { echo "FAIL: не пробрасываются заголовки контекста"; exit 1; }
# Внутренняя зона: ext-auth и скрейп идут на порт internal, а не на публичный.
grep -A2 'backendRefs:' <<< "$out" | grep -q 'port: 8081' \
  || { echo "FAIL: ext-auth не смотрит на внутренний порт 8081"; exit 1; }
grep -A1 'endpoints:' <<< "$out" | grep -q 'port: internal' \
  || { echo "FAIL: ServiceMonitor не скрейпит порт internal"; exit 1; }
echo "PASS: маршруты, политика ext-auth и ServiceMonitor на внутреннем порту, HPA на месте"

echo "== рендер оверлея стенда"
out=$(helm template ci "$CHART" -f "$CHART/values-local.yaml")
for component in postgresql redis rabbitmq keycloak echo stub-subgraph stub-consumer messenger; do
  has "component: $component" || { echo "FAIL: в стенде нет $component"; exit 1; }
done
# messenger доверяет x-identity-* — политика пускает к нему только router.
netpols=$(grep -c 'kind: NetworkPolicy' <<< "$out" || true)
[ "$netpols" -ge 2 ] || { echo "FAIL: NetworkPolicy в стенде $netpols, ожидалось ≥2 (identity-service, messenger)"; exit 1; }
grep -A3 'kind: NetworkPolicy' <<< "$out" | grep -q 'name: ci-identity-service-messenger$' || { echo "FAIL: нет NetworkPolicy для messenger"; exit 1; }
# У echo-upstream стенда своя SecurityPolicy — она тоже обязана звать
# ext-auth на внутренний порт: на публичном validate теперь 404, а Envoy
# отдаёт клиенту код ответа auth-сервиса, и стенд ловил это как 404 вместо 403.
out=$(helm template ci "$CHART" -f "$CHART/values-local.yaml" -s templates/debug-echo.yaml)
has "port: 8081" || { echo "FAIL: ext-auth echo-политики не смотрит на внутренний порт"; exit 1; }
has "port: 8080" && { echo "FAIL: echo-политика ссылается на публичный порт"; exit 1; }
has "component: router" && { echo "FAIL: router больше не зависимость чарта identity-service"; exit 1; }
echo "PASS: стенд рендерит все зависимости, echo-политика на внутреннем порту"

echo "== чарт graphql-router"
helm lint charts/graphql-router
out=$(helm template ci charts/graphql-router --set fullnameOverride=ci-graphql-router)
has "kind: Deployment" || { echo "FAIL: нет Deployment router"; exit 1; }
has "kind: Service" || { echo "FAIL: нет Service router"; exit 1; }
has "name: ci-graphql-router-config" || { echo "FAIL: ConfigMap композиции не подключён по умолчанию"; exit 1; }
has "app.kubernetes.io/name: graphql-router" || { echo "FAIL: метка graphql-router, на которую ссылается NetworkPolicy"; exit 1; }
# Router пробрасывает доверенный контекст — вход к нему только из namespace gateway.
has "kind: NetworkPolicy" || { echo "FAIL: нет NetworkPolicy у router"; exit 1; }
has 'kubernetes.io/metadata.name: "envoy-gateway-system"' || { echo "FAIL: NetworkPolicy router не ссылается на namespace gateway"; exit 1; }
out=$(helm template ci charts/graphql-router --set configMapName=custom-config)
has "name: custom-config" || { echo "FAIL: configMapName не переопределяется"; exit 1; }
echo "PASS: чарт router рендерится, ConfigMap композиции подключаем"
