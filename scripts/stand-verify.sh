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

# Два пода в namespace стенда: с метками разрешённого источника и без
# меток — как любой чужой под. Долгоживущие, а не `kubectl run -i`: CNI
# добавляет адрес нового пода в набор политики асинхронно, одноразовый
# под стреляет раньше и получает отказ при верных метках; после Ready
# адрес учтён. Печатают HTTP-код, 000 — соединения нет.
PROBE_ALLOWED="probe-allowed-$RANDOM"
PROBE_PLAIN="probe-plain-$RANDOM"
probe_cleanup_pods() { kubectl delete pod -n "$NS" "$PROBE_ALLOWED" "$PROBE_PLAIN" --ignore-not-found --wait=false >/dev/null 2>&1 || true; }
trap 'probe_cleanup_pods; rm -f "$COOKIE_JAR"' EXIT
kubectl run "$PROBE_ALLOWED" -n "$NS" --restart=Never --image=curlimages/curl:latest \
  --labels=identity-stand/tools=true,app.kubernetes.io/component=router --command -- sleep 600 >/dev/null
kubectl run "$PROBE_PLAIN" -n "$NS" --restart=Never --image=curlimages/curl:latest --command -- sleep 600 >/dev/null
kubectl wait pod/"$PROBE_ALLOWED" pod/"$PROBE_PLAIN" -n "$NS" --for=condition=Ready --timeout=60s >/dev/null
probe_curl() { kubectl exec -n "$NS" "$1" -- curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$2" 2>/dev/null || true; }
SVC="http://$RELEASE-identity-service"

step "11. Внутренняя зона отсутствует на публичном порту"
code=$(probe_curl "$PROBE_ALLOWED" "$SVC:8080/internal/session/validate")
[ "$code" = 404 ] || fail "validate на :8080 -> $code, ожидалось 404 (зона не смонтирована)"
code_m=$(probe_curl "$PROBE_ALLOWED" "$SVC:8080/internal/metrics")
[ "$code_m" = 404 ] || fail "metrics на :8080 -> $code_m, ожидалось 404"
pass "validate и metrics на публичном порту -> 404"

step "12. Внутренний порт закрыт сетевой политикой"
code=$(probe_curl "$PROBE_PLAIN" "$SVC:8081/internal/session/validate")
[ "$code" = 000 ] || fail "validate на :8081 из немаркированного пода -> $code, ожидалось отсутствие соединения"
code=$(probe_curl "$PROBE_PLAIN" "$SVC:8080/healthz")
[ "$code" = 000 ] || fail "публичный порт из немаркированного пода -> $code, ожидалось отсутствие соединения"
code=$(probe_curl "$PROBE_ALLOWED" "$SVC:8081/internal/session/validate")
[ "$code" = 401 ] || fail "validate на :8081 из разрешённого пода без cookie -> $code, ожидалось 401"
code=$(probe_curl "$PROBE_ALLOWED" "$SVC:8081/internal/metrics")
[ "$code" = 200 ] || fail "metrics на :8081 из разрешённого пода -> $code, ожидалось 200"
pass "чужой под: соединения нет на оба порта; разрешённый под: validate 401 без cookie, metrics 200"

step "13. Подделанные заголовки контекста перезаписываются на proxy"
# У qa-пользователя стенда нет ролей — validate отдаёт ПУСТОЙ заголовок
# прав; он обязан вытеснить подделанный, иначе пустое значение — дыра.
forged=$(curl -s "${RESOLVE[@]}" "${COOKIE[@]}" \
  -H 'x-identity-user-id: 00000000-0000-4000-8000-00000000f0f0' \
  -H 'x-identity-email: forged@example.com' \
  -H 'x-identity-permissions: gsh:admin.access' \
  "http://$HOST/debug/echo")
grep -q 'f0f0\|forged@example.com\|gsh:admin.access' <<< "$forged" && fail "подделанный заголовок дошёл до upstream: $forged"
grep -q "$uid" <<< "$forged" || fail "upstream не получил id сессии: $forged"
grep -qi '"x-identity-permissions":""' <<< "$forged" || fail "пустой заголовок прав не заменил подделанный: $forged"
pass "upstream получил id сессии и пустые права; подделка не прошла"

step "14. Первая установка чарта: хук миграций в пустом namespace"
# Стенд держит зависимости в том же релизе и хук там выключен; здесь чарт
# ставится как в проде — в пустой namespace, с внешней базой (отдельная БД
# в PostgreSQL стенда) — и хук обязан отработать сам, без ресурсов релиза.
PG="deploy/$RELEASE-identity-service-postgresql"
PROBE_NS="hook-probe"
PROBE_DB="identity_hookprobe"
probe_psql() { kubectl exec -n "$NS" "$PG" -- psql -U identity -d "$1" -tAc "$2" 2>/dev/null | tr -d '[:space:]'; }
probe_cleanup() {
  helm uninstall hook-probe -n "$PROBE_NS" >/dev/null 2>&1 || true
  kubectl delete namespace "$PROBE_NS" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  probe_psql identity_development "DROP DATABASE IF EXISTS $PROBE_DB WITH (FORCE)" >/dev/null || true
}
probe_cleanup
kubectl delete namespace "$PROBE_NS" --ignore-not-found --wait=true >/dev/null 2>&1 || true
# Уборка — в EXIT-trap до создания первого ресурса: любой fail, set -e или
# прерывание иначе оставили бы БД, namespace и релиз (ревью Codex, PR #4).
trap 'probe_cleanup; probe_cleanup_pods; rm -f "$COOKIE_JAR"' EXIT
probe_psql identity_development "CREATE DATABASE $PROBE_DB" >/dev/null || fail "не удалось создать БД $PROBE_DB"
kubectl create namespace "$PROBE_NS" >/dev/null
kubectl create secret generic hook-probe-secret -n "$PROBE_NS" \
  --from-literal=DATABASE_URL="postgres://identity:identity@$RELEASE-identity-service-postgresql.$NS.svc:5432/$PROBE_DB?sslmode=disable" >/dev/null
# APP_ENV=development: подкоманде migrate нужен только DATABASE_URL, остальные
# обязательные переменные прода к миграциям не относятся.
install_out=$(helm install hook-probe charts/identity-service -n "$PROBE_NS" \
  --set existingSecret=hook-probe-secret \
  --set image.repository=identity-service --set image.tag=dev --set image.pullPolicy=Never \
  --set config.APP_ENV=development --timeout 3m 2>&1) \
  || fail "helm install в пустой namespace не прошёл: $(tail -3 <<< "$install_out")"
# Эталон — число миграций в чекауте: stand-up собирает образ из него же,
# и именно этот образ запускает хук. Основная база стенда эталоном быть не
# может: при чередовании веток она бывает впереди текущей.
expected=$(ls db/migrations/*.sql | wc -l | tr -d ' ')
version=$(probe_psql "$PROBE_DB" "SELECT max(version_id) FROM goose_db_version")
[ "$version" = "$expected" ] || fail "после первой установки версия схемы $version, ожидалась $expected"
pass "helm install с нуля: хук отработал сам, версия схемы $version"

upgrade_out=$(helm upgrade hook-probe charts/identity-service -n "$PROBE_NS" \
  --set existingSecret=hook-probe-secret \
  --set image.repository=identity-service --set image.tag=dev --set image.pullPolicy=Never \
  --set config.APP_ENV=development --set config.LOG_LEVEL=debug --timeout 3m 2>&1) \
  || fail "helm upgrade с изменённой конфигурацией не прошёл: $(tail -3 <<< "$upgrade_out")"
revision=$(helm history hook-probe -n "$PROBE_NS" --max 1 -o json 2>/dev/null | grep -oE '"revision":[0-9]+' | grep -oE '[0-9]+' || true)
jobs_created=$(kubectl get events -n "$PROBE_NS" --field-selector reason=SuccessfulCreate -o json 2>/dev/null | grep -c 'hook-probe-identity-service-migrate' || true)
[ "$revision" = 2 ] && [ "$jobs_created" -ge 2 ] || fail "upgrade: revision=$revision, запусков Job миграций=$jobs_created"
pass "helm upgrade: revision 2, хук миграций отработал повторно"
probe_cleanup
trap 'probe_cleanup_pods; rm -f "$COOKIE_JAR"' EXIT

printf '\nСТЕНД ПРОВЕРЕН ЦЕЛИКОМ\n' 
