#!/usr/bin/env bash
# Композиция суперграфа для стенда: тянет SDL обоих сабграфов из кластера
# и кладёт execution config плюс конфигурацию router'а в ConfigMap.
#
# На стенде композиция статическая: schema registry (Cosmo Studio) не нужен,
# а перекомпозиция — это перезапуск router'а.
set -euo pipefail

NS="${NS:-identity-stand}"
RELEASE="${RELEASE:-identity-stand}"
E2E_TOKEN="${E2E_TOKEN:-local-dev-e2e-token-0123456789abcdef}"
USER_EMAIL="${USER_EMAIL:-stand-qa@example.com}"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# Служебный пользователь для e2e-входа. CLI делает upsert по email, поэтому
# шаг идемпотентен; без него composition зависела бы от того, кто и когда
# завёл пользователя руками (база стенда эфемерна).
kubectl run "create-user-$RANDOM" -n "$NS" --rm -i --restart=Never --quiet \
  --image=identity-service:dev --image-pull-policy=Never \
  --overrides="{\"spec\":{\"containers\":[{\"name\":\"cli\",\"image\":\"identity-service:dev\",\"imagePullPolicy\":\"Never\",\"args\":[\"create-user\",\"--email\",\"$USER_EMAIL\",\"--name\",\"SDL probe\"],\"envFrom\":[{\"configMapRef\":{\"name\":\"$RELEASE-identity-service\"}}]}]}}" >/dev/null

# SDL нашего сабграфа отдаётся только аутентифицированным — берём сессию
# через e2e-endpoint, он для этого и существует. Endpoint живёт во
# внутренней зоне (:8081), куда NetworkPolicy пускает только поды с меткой
# tools; SDL — публичная зона, туда под пускают как router.
#
# Под долгоживущий, а не `kubectl run -i`: CNI добавляет адрес нового пода
# в разрешённый набор политики асинхронно, и одноразовый под успевает
# отстреляться раньше — соединение отклоняется, хотя метки верные.
# После Ready адрес уже учтён (проверено на стенде).
POD="sdl-probe-$RANDOM"
kubectl delete pod -n "$NS" -l identity-stand/probe=sdl --ignore-not-found --wait=true >/dev/null 2>&1
kubectl run "$POD" -n "$NS" --restart=Never --image=curlimages/curl:latest \
  --labels=identity-stand/probe=sdl,identity-stand/tools=true,app.kubernetes.io/component=router \
  --command -- sleep 300 >/dev/null
trap 'kubectl delete pod -n "$NS" "$POD" --ignore-not-found --wait=false >/dev/null 2>&1; rm -rf "$WORK"' EXIT
kubectl wait pod/"$POD" -n "$NS" --for=condition=Ready --timeout=60s >/dev/null
kubectl exec -n "$NS" "$POD" -- sh -c "
C=\$(curl -s -D - -o /dev/null -X POST http://$RELEASE-identity-service:8081/internal/e2e/login \
  -H 'Authorization: Bearer $E2E_TOKEN' -d '{\"email\":\"$USER_EMAIL\"}' \
  | grep -i '^set-cookie' | sed 's/.*__identity_session=\([^;]*\).*/\1/')
curl -s -X POST http://$RELEASE-identity-service:8080/graphql -b \"__identity_session=\$C\" \
  -H 'Content-Type: application/json' -d '{\"query\":\"{ _service { sdl } }\"}'" 2>/dev/null > "$WORK/identity.json"

python3 -c "
import json,sys
print(json.load(open('$WORK/identity.json'))['data']['_service']['sdl'])
" > "$WORK/identity.graphql"

cp dev/stub-subgraph/schema.graphqls "$WORK/orders.graphql"

cat > "$WORK/compose.yaml" <<YAML
version: 1
subgraphs:
  - name: identity
    routing_url: http://$RELEASE-identity-service:8080/graphql
    schema:
      file: $WORK/identity.graphql
  - name: orders
    routing_url: http://$RELEASE-identity-service-stub-subgraph:8080/graphql
    schema:
      file: $WORK/orders.graphql
YAML

npx --yes wgc@latest router compose -i "$WORK/compose.yaml" -o "$WORK/supergraph.json" | tail -1

cat > "$WORK/config.yaml" <<'YAML'
version: "1"
listen_addr: "0.0.0.0:3002"
dev_mode: true
execution_config:
  file:
    path: /etc/router/supergraph.json
headers:
  all:
    request:
      # Cookie — то, чем router аутентифицируется в identity-сабграфе:
      # у нас нет отдельного machine-канала, сабграф проверяет сессию.
      - op: propagate
        named: cookie
      # Браузерный Origin: сабграф проверяет его на мутациях (CSRF), и без
      # проброса каждый запрос выглядел бы как не-браузерный.
      - op: propagate
        named: origin
      # Контекст от ext-auth — бизнес-сабграфам.
      - op: propagate
        named: x-identity-user-id
      - op: propagate
        named: x-identity-email
      - op: propagate
        named: x-identity-permissions
YAML

kubectl create configmap router-config -n "$NS" \
  --from-file=supergraph.json="$WORK/supergraph.json" \
  --from-file=config.yaml="$WORK/config.yaml" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl rollout restart "deploy/$RELEASE-identity-service-router" -n "$NS" >/dev/null 2>&1 || true
echo "суперграф скомпонован и загружен"
