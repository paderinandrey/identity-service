#!/usr/bin/env bash
# Проверка локального стенда: доказывает контур аутентификации целиком —
# от формы IdP до заголовков контекста, дошедших до защищённого upstream.
#
# Печатает пошаговые PASS с наблюдёнными значениями, а не «всё хорошо»:
# по каждой строке видно, что именно проверено.
set -euo pipefail

NS="${NS:-identity-stand}"
RELEASE="${RELEASE:-identity-stand}"
HOST="${HOST:-identity.localtest.me}"
KC_HOST="${KC_HOST:-keycloak.localtest.me}"
SCIM_TOKEN="${SCIM_TOKEN:-local-dev-scim-token-0123456789abcdef}"
USER_EMAIL="${USER_EMAIL:-stand-qa@example.com}"
COOKIE_JAR=$(mktemp)
trap 'rm -f "$COOKIE_JAR"' EXIT

GW=$(kubectl get gateway -n "$NS" identity-gateway -o jsonpath='{.status.addresses[0].value}')
[ -n "$GW" ] || { echo "FAIL: gateway has no address"; exit 1; }
RESOLVE=(--resolve "$HOST:80:$GW" --resolve "$KC_HOST:80:$GW")
echo "gateway: $GW"

step() { printf '\n== %s\n' "$1"; }
pass() { printf 'PASS: %s\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1"; exit 1; }

step "1. Публичные маршруты открыты без сессии"
code=$(curl -s -o /dev/null -w '%{http_code}' "${RESOLVE[@]}" "http://$HOST/auth/saml/metadata")
[ "$code" = 200 ] || fail "SP metadata -> $code, ожидалось 200"
pass "GET /auth/saml/metadata -> 200"

step "2. Внутренние пути не опубликованы наружу"
code=$(curl -s -o /dev/null -w '%{http_code}' "${RESOLVE[@]}" "http://$HOST/internal/session/validate")
[ "$code" = 404 ] || fail "/internal/... -> $code, ожидалось 404 (маршрута нет)"
pass "GET /internal/session/validate -> 404 (наружу не маршрутизируется)"

step "3. Защищённый upstream закрыт без сессии — отказ на proxy"
code=$(curl -s -o /dev/null -w '%{http_code}' "${RESOLVE[@]}" "http://$HOST/debug/echo")
[ "$code" = 403 ] || [ "$code" = 401 ] || fail "/debug/echo без cookie -> $code, ожидался отказ"
# Access-логи Envoy сбрасываются с задержкой — ищем запись про этот путь
# в последних строках, с коротким ретраем, а не в самой последней строке.
denied=""
for _ in 1 2 3 4 5 6; do
  denied=$(kubectl logs -n envoy-gateway-system -l gateway.envoyproxy.io/owning-gateway-name=identity-gateway --tail=30 2>/dev/null \
    | grep '"x-envoy-origin-path":"/debug/echo"' | grep ext_authz_denied | tail -1 || true)
  [ -n "$denied" ] && break
  sleep 2
done
[ -n "$denied" ] || fail "в логах Envoy нет записи ext_authz_denied для /debug/echo"
echo "$denied" | grep -q '"upstream_host":null' || fail "запрос всё же дошёл до upstream"
pass "/debug/echo без cookie -> $code, envoy: ext_authz_denied, upstream_host: null"

step "4. Провижининг пользователя через SCIM"
curl -s -o /dev/null "${RESOLVE[@]}" -X POST "http://$HOST/scim/v2/Users" \
  -H "Authorization: Bearer $SCIM_TOKEN" -H 'Content-Type: application/scim+json' \
  -d "{\"schemas\":[\"urn:ietf:params:scim:schemas:core:2.0:User\"],\"userName\":\"$USER_EMAIL\",\"displayName\":\"Stand QA\",\"active\":true}" || true
found=$(curl -s "${RESOLVE[@]}" -H "Authorization: Bearer $SCIM_TOKEN" \
  "http://$HOST/scim/v2/Users?filter=userName%20eq%20%22$USER_EMAIL%22" | grep -c "$USER_EMAIL" || true)
[ "$found" -ge 1 ] || fail "пользователь $USER_EMAIL не найден через SCIM"
pass "пользователь $USER_EMAIL заведён и находится фильтром SCIM"

step "5. Вход через форму Keycloak (полный SAML-цикл)"
kubectl exec -n "$NS" deploy/"$RELEASE"-identity-service-keycloak -- \
  /opt/keycloak/bin/kcadm.sh config credentials --server http://localhost:8080 --realm master --user admin --password admin >/dev/null 2>&1
kubectl exec -n "$NS" deploy/"$RELEASE"-identity-service-keycloak -- \
  /opt/keycloak/bin/kcadm.sh create users -r identity -s username="$USER_EMAIL" -s email="$USER_EMAIL" \
  -s emailVerified=true -s enabled=true -s firstName=Stand -s lastName=QA >/dev/null 2>&1 || true
kubectl exec -n "$NS" deploy/"$RELEASE"-identity-service-keycloak -- \
  /opt/keycloak/bin/kcadm.sh set-password -r identity --username "$USER_EMAIL" --new-password password >/dev/null 2>&1

sso=$(curl -s -o /dev/null -w '%{redirect_url}' "${RESOLVE[@]}" -c "$COOKIE_JAR" "http://$HOST/auth/saml/init?next=/debug/echo")
echo "$sso" | grep -q "$KC_HOST" || fail "init не увёл на IdP: $sso"
form=$(curl -s "${RESOLVE[@]}" -b "$COOKIE_JAR" -c "$COOKIE_JAR" "$sso")
action=$(echo "$form" | grep -oE 'id="kc-form-login"[^>]*action="[^"]*"' | grep -oE 'action="[^"]*"' | sed 's/action="//; s/"$//' | sed 's/&amp;/\&/g')
[ -n "$action" ] || fail "форма входа Keycloak не найдена"
saml_page=$(curl -s "${RESOLVE[@]}" -b "$COOKIE_JAR" -c "$COOKIE_JAR" -X POST "$action" \
  --data-urlencode "username=$USER_EMAIL" --data-urlencode "password=password")
saml_response=$(echo "$saml_page" | grep -oE 'name="SAMLResponse" value="[^"]*"' | sed 's/.*value="//; s/"$//')
relay=$(echo "$saml_page" | grep -oE 'name="RelayState" value="[^"]*"' | sed 's/.*value="//; s/"$//' | sed 's/&#x2F;/\//g')
[ -n "$saml_response" ] || fail "Keycloak не вернул SAMLResponse"
acs_redirect=$(curl -s -o /dev/null -w '%{redirect_url}' "${RESOLVE[@]}" -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
  -X POST "http://$HOST/auth/saml/acs" --data-urlencode "SAMLResponse=$saml_response" --data-urlencode "RelayState=$relay")
grep -q '__identity_session' "$COOKIE_JAR" || fail "сессионная cookie не выдана"
pass "вход выполнен, cookie выдана, ACS увёл на $acs_redirect"

step "6. Заголовки контекста доходят до защищённого upstream"
echoed=$(curl -s "${RESOLVE[@]}" -b "$COOKIE_JAR" "http://$HOST/debug/echo")
for header in x-identity-user-id x-identity-email; do
  echo "$echoed" | tr 'A-Z' 'a-z' | grep -q "$header" || fail "upstream не увидел $header"
done
uid=$(echo "$echoed" | tr ',' '\n' | grep -i 'x-identity-user-id' | head -1)
mail=$(echo "$echoed" | tr ',' '\n' | grep -i 'x-identity-email' | head -1)
pass "upstream получил: $uid $mail"

step "7. /auth/me через proxy"
me=$(curl -s "${RESOLVE[@]}" -b "$COOKIE_JAR" "http://$HOST/auth/me")
echo "$me" | grep -q "$USER_EMAIL" || fail "/auth/me вернул: $me"
pass "/auth/me -> $me"

step "8. Федерация: запрос через router в оба сабграфа"
fed=$(curl -s "${RESOLVE[@]}" -b "$COOKIE_JAR" -X POST "http://$HOST/graphql" \
  -H 'Content-Type: application/json' \
  -d '{"query":"{ orders { id seenIdentityHeaders owner { id email } } }"}')
echo "$fed" | grep -q '"x-identity-user-id=' || fail "сабграф не получил контекст: $fed"
echo "$fed" | grep -q "$USER_EMAIL" || fail "federation-ссылка owner -> User не разрешилась: $fed"
pass "router собрал ответ из двух сабграфов; стаб получил контекст, owner разрешён в identity"

step "9. Аноним не доходит до router"
code=$(curl -s -o /dev/null -w '%{http_code}' "${RESOLVE[@]}" -X POST "http://$HOST/graphql" \
  -H 'Content-Type: application/json' -d '{"query":"{ __schema { types { name } } }"}')
[ "$code" = 401 ] || [ "$code" = 403 ] || fail "анонимная introspection -> $code, ожидался отказ"
pass "анонимная introspection -> $code (схема не раскрывается)"

printf '\nСТЕНД ПРОВЕРЕН ЦЕЛИКОМ\n'
