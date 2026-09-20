#!/usr/bin/env bash
# Заводит пользователя стенда в identity-service через SCIM так, как это
# делает Okta: externalId = идентификатор пользователя в IdP. Тот же UUID
# Keycloak отдаёт как persistent NameID при SAML-входе, поэтому без этого
# шага вход даёт 401 — связки по email больше нет, ключ только один.
#
# Идемпотентен: существующего пользователя обновляет PATCH'ем. Печатает
# в stdout id пользователя в Keycloak; диагностика — в stderr.
set -euo pipefail

NS="${NS:-identity-stand}"
RELEASE="${RELEASE:-identity-stand}"
HOST="${HOST:-identity.localhost}"
SCIM_TOKEN="${SCIM_TOKEN:-local-dev-scim-token-0123456789abcdef}"
USER_EMAIL="${USER_EMAIL:-qa@example.com}"
KC="deploy/$RELEASE-identity-service-keycloak"

GW=$(kubectl get gateway -n "$NS" identity-gateway -o jsonpath='{.status.addresses[0].value}')
[ -n "$GW" ] || { echo "FAIL: gateway has no address" >&2; exit 1; }
curl_scim() { curl -s --resolve "$HOST:80:$GW" -H "Authorization: Bearer $SCIM_TOKEN" -H 'Content-Type: application/scim+json' "$@"; }

kubectl exec -n "$NS" "$KC" -- /opt/keycloak/bin/kcadm.sh config credentials \
  --server http://localhost:8080 --realm master --user admin --password admin >/dev/null 2>&1
KC_USER_ID=$(kubectl exec -n "$NS" "$KC" -- /opt/keycloak/bin/kcadm.sh get users -r identity \
  -q username="$USER_EMAIL" --fields id 2>/dev/null \
  | grep -oE '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}' | head -1)
[ -n "$KC_USER_ID" ] || { echo "FAIL: пользователь $USER_EMAIL не найден в Keycloak" >&2; exit 1; }

# Keycloak не умеет отдавать собственный id пользователя как NameID
# напрямую (persistent у него — псевдоним на клиента), поэтому realm
# объявляет атрибут stableId и маппер NameID на него. Значение = id
# пользователя, то же, что SCIM-плагин шлёт как externalId. У Okta это
# делается выражением user.id без атрибутов.
kubectl exec -n "$NS" "$KC" -- /opt/keycloak/bin/kcadm.sh update "users/$KC_USER_ID" -r identity \
  -s "attributes.stableId=$KC_USER_ID" >/dev/null 2>&1 \
  || { echo "FAIL: не удалось задать stableId пользователю $USER_EMAIL" >&2; exit 1; }

encoded_filter="userName%20eq%20%22$USER_EMAIL%22"
existing=$(curl_scim "http://$HOST/scim/v2/Users?filter=$encoded_filter" \
  | grep -oE '"id":"[^"]+"' | head -1 | cut -d'"' -f4 || true)

if [ -n "$existing" ]; then
  code=$(curl_scim -o /dev/null -w '%{http_code}' -X PATCH "http://$HOST/scim/v2/Users/$existing" \
    -d "{\"schemas\":[\"urn:ietf:params:scim:api:messages:2.0:PatchOp\"],\"Operations\":[{\"op\":\"replace\",\"value\":{\"externalId\":\"$KC_USER_ID\",\"active\":true}}]}")
  [ "$code" = 200 ] || { echo "FAIL: PATCH externalId -> $code" >&2; exit 1; }
  echo "обновлён $USER_EMAIL (id $existing): externalId = $KC_USER_ID" >&2
else
  code=$(curl_scim -o /dev/null -w '%{http_code}' -X POST "http://$HOST/scim/v2/Users" \
    -d "{\"schemas\":[\"urn:ietf:params:scim:schemas:core:2.0:User\"],\"userName\":\"$USER_EMAIL\",\"displayName\":\"QA User\",\"title\":\"QA Engineer\",\"externalId\":\"$KC_USER_ID\",\"active\":true}")
  [ "$code" = 201 ] || { echo "FAIL: POST user -> $code" >&2; exit 1; }
  echo "создан $USER_EMAIL: externalId = $KC_USER_ID" >&2
fi
echo "$KC_USER_ID"
