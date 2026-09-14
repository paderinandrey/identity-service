#!/usr/bin/env bash
# Проверка локального стенда: доказывает контур аутентификации целиком —
# от формы IdP до заголовков контекста, дошедших до защищённого upstream.
#
# Печатает пошаговые PASS с наблюдёнными значениями, а не «всё хорошо»:
# по каждой строке видно, что именно проверено.
set -euo pipefail

NS="${NS:-identity-stand}"
RELEASE="${RELEASE:-identity-stand}"
HOST="${HOST:-identity.localhost}"
KC_HOST="${KC_HOST:-keycloak.localhost}"
SCIM_TOKEN="${SCIM_TOKEN:-local-dev-scim-token-0123456789abcdef}"
# Пользователь из импортируемого realm: у него уже есть пароль, и он
# переживает рестарт Keycloak (dev-хранилище эфемерное, realm — нет).
USER_EMAIL="${USER_EMAIL:-qa@example.com}"
COOKIE_JAR=$(mktemp)
trap 'rm -f "$COOKIE_JAR"' EXIT

GW=$(kubectl get gateway -n "$NS" identity-gateway -o jsonpath='{.status.addresses[0].value}')
[ -n "$GW" ] || { echo "FAIL: gateway has no address"; exit 1; }
RESOLVE=(--resolve "$HOST:80:$GW" --resolve "$KC_HOST:80:$GW")
echo "gateway: $GW"

# Проверки подстрок — через here-string, а не `echo | grep -q`: grep -q
# закрывает пайп на первом совпадении, echo ловит SIGPIPE, и pipefail
# роняет успешную проверку. Ловилось в CI на чарт-скрипте.
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
grep -q '"upstream_host":null' <<< "$denied" || fail "запрос всё же дошёл до upstream"
pass "/debug/echo без cookie -> $code, envoy: ext_authz_denied, upstream_host: null"

step "4. Провижининг пользователя через SCIM с externalId из IdP"
KC_USER_ID=$(./scripts/stand-provision-user.sh 2>/tmp/stand-provision.err) \
  || fail "провижининг не прошёл: $(cat /tmp/stand-provision.err)"
listed=$(curl -s "${RESOLVE[@]}" -H "Authorization: Bearer $SCIM_TOKEN" \
  "http://$HOST/scim/v2/Users?filter=userName%20eq%20%22$USER_EMAIL%22")
grep -q "\"externalId\":\"$KC_USER_ID\"" <<< "$listed" || fail "SCIM не вернул externalId = $KC_USER_ID: $listed"
pass "пользователь $USER_EMAIL заведён, externalId = id в Keycloak ($KC_USER_ID)"

step "5. Вход через форму Keycloak (полный SAML-цикл)"
# curl отбрасывает Secure-куки Keycloak по http, а браузер на *.localhost
# их принимает (loopback — secure context). Helper воспроизводит именно
# браузерное поведение, поэтому проверяется настоящий путь пользователя.
SESSION=$(./scripts/stand-login.py "$HOST" "$USER_EMAIL" password /debug/echo 2>/tmp/stand-login.err) \
  || fail "вход не прошёл: $(cat /tmp/stand-login.err)"
pass "$(cat /tmp/stand-login.err)"
COOKIE=(-b "__identity_session=$SESSION")

step "6. Subject входа — стабильный id IdP, а не email"
subject=$(kubectl exec -n "$NS" "deploy/$RELEASE-identity-service-postgresql" -- \
  psql -U identity -d identity_development -tAc \
  "SELECT i.subject FROM user_identities i JOIN users u ON u.id = i.user_id WHERE u.email = '$USER_EMAIL' AND i.provider = 'okta'" 2>/dev/null | tr -d '[:space:]')
[ "$subject" = "$KC_USER_ID" ] || fail "привязка okta: subject=$subject, ожидался id из Keycloak $KC_USER_ID"
pass "привязка okta: subject = $subject = persistent NameID = SCIM externalId; email не участвует"

step "7. Заголовки контекста доходят до защищённого upstream"
echoed=$(curl -s "${RESOLVE[@]}" "${COOKIE[@]}" "http://$HOST/debug/echo")
for header in x-identity-user-id x-identity-email; do
  grep -qi "$header" <<< "$echoed" || fail "upstream не увидел $header"
done
uid=$(echo "$echoed" | tr ',' '\n' | grep -i 'x-identity-user-id' | head -1)
mail=$(echo "$echoed" | tr ',' '\n' | grep -i 'x-identity-email' | head -1)
pass "upstream получил: $uid $mail"

step "8. /auth/me через proxy"
me=$(curl -s "${RESOLVE[@]}" "${COOKIE[@]}" "http://$HOST/auth/me")
grep -q "$USER_EMAIL" <<< "$me" || fail "/auth/me вернул: $me"
pass "/auth/me -> $me"

step "9. Федерация: запрос через router в оба сабграфа"
fed=$(curl -s "${RESOLVE[@]}" "${COOKIE[@]}" -X POST "http://$HOST/graphql" \
  -H 'Content-Type: application/json' \
  -d '{"query":"{ orders { id seenIdentityHeaders owner { id email } } }"}')
grep -q '"x-identity-user-id=' <<< "$fed" || fail "сабграф не получил контекст: $fed"
grep -q "$USER_EMAIL" <<< "$fed" || fail "federation-ссылка owner -> User не разрешилась: $fed"
pass "router собрал ответ из двух сабграфов; стаб получил контекст, owner разрешён в identity"

step "10. Аноним не доходит до router"
code=$(curl -s -o /dev/null -w '%{http_code}' "${RESOLVE[@]}" -X POST "http://$HOST/graphql" \
  -H 'Content-Type: application/json' -d '{"query":"{ __schema { types { name } } }"}')
[ "$code" = 401 ] || [ "$code" = 403 ] || fail "анонимная introspection -> $code, ожидался отказ"
pass "анонимная introspection -> $code (схема не раскрывается)"

printf '\nСТЕНД ПРОВЕРЕН ЦЕЛИКОМ\n'
