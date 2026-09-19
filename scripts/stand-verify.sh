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

step "5. Проекция консьюмера: replay, живое изменение, повторный replay"
# Референсный консьюмер (dev/stub-consumer) — образец поведения для
# GSH/DFM. Его проекция читается через port-forward: он внутри кластера.
CONSUMER_PORT=18090
kubectl port-forward -n "$NS" "svc/$RELEASE-identity-service-stub-consumer" "$CONSUMER_PORT:8080" >/dev/null 2>&1 &
PF_PID=$!
trap 'kill $PF_PID 2>/dev/null; rm -f "$COOKIE_JAR"' EXIT
sleep 2
consumer() { curl -s --max-time 5 "http://127.0.0.1:$CONSUMER_PORT$1"; }
replay() {
  kubectl run "replay-$RANDOM" -n "$NS" --rm -i --restart=Never --quiet \
    --image=identity-service:dev --image-pull-policy=Never \
    --overrides="{\"spec\":{\"containers\":[{\"name\":\"cli\",\"image\":\"identity-service:dev\",\"imagePullPolicy\":\"Never\",\"args\":[\"replay-users\"],\"envFrom\":[{\"configMapRef\":{\"name\":\"$RELEASE-identity-service\"}}]}]}}" 2>/dev/null | tail -1
}
# Эталон — база: каждый пользователь с его текущей версией.
db_users() {
  kubectl exec -n "$NS" "deploy/$RELEASE-identity-service-postgresql" -- \
    psql -U identity -d identity_development -tAc "SELECT id || ':' || version FROM users ORDER BY id" 2>/dev/null | tr -d ' '
}
projection_users() {
  local tmp; tmp=$(mktemp)
  consumer /projection > "$tmp"
  python3 -c "import json,sys; d=json.load(open(sys.argv[1])); print('\n'.join(u['id']+':'+str(u['version']) for u in sorted(d['users'], key=lambda u: u['id'])))" "$tmp"
  rm -f "$tmp"
}
expected=$(db_users)
[ -n "$expected" ] || fail "в базе нет пользователей"
out=$(replay); grep -q "snapshot event(s) enqueued" <<< "$out" || fail "replay-users не отработал: $out"
for _ in $(seq 1 30); do [ "$(projection_users)" = "$expected" ] && break; sleep 1; done
[ "$(projection_users)" = "$expected" ] || fail "после replay проекция $(projection_users | tr '\n' ' ') != база $(echo "$expected" | tr '\n' ' ')"
n_users=$(wc -l <<< "$expected" | tr -d ' ')
pass "replay на пустую проекцию: $n_users пользователь(ей) с версиями из базы"

# Живое изменение: SCIM PATCH имени доходит до проекции с версией +1.
scim_id=$(curl -s "${RESOLVE[@]}" -H "Authorization: Bearer $SCIM_TOKEN" \
  "http://$HOST/scim/v2/Users?filter=userName%20eq%20%22$USER_EMAIL%22" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["Resources"][0]["id"])')
before_version=$(consumer "/projection/$scim_id" | python3 -c 'import json,sys; print(json.load(sys.stdin)["version"])')
new_name="QA User $RANDOM"
code=$(curl -s "${RESOLVE[@]}" -o /dev/null -w '%{http_code}' -X PATCH -H "Authorization: Bearer $SCIM_TOKEN" \
  -H 'Content-Type: application/scim+json' "http://$HOST/scim/v2/Users/$scim_id" \
  -d "{\"schemas\":[\"urn:ietf:params:scim:api:messages:2.0:PatchOp\"],\"Operations\":[{\"op\":\"replace\",\"value\":{\"displayName\":\"$new_name\"}}]}")
[ "$code" = 200 ] || fail "SCIM PATCH имени -> $code"
for _ in $(seq 1 30); do
  projected=$(consumer "/projection/$scim_id")
  grep -q "\"name\":\"$new_name\"" <<< "$projected" && break
  sleep 1
done
grep -q "\"name\":\"$new_name\"" <<< "$projected" || fail "изменение имени не дошло до проекции: $projected"
after_version=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["version"])' <<< "$projected")
[ "$after_version" = "$((before_version + 1))" ] || fail "версия в проекции $after_version, ожидалась $((before_version + 1))"
pass "SCIM-изменение имени в проекции с версией $after_version (было $before_version)"

# Повторный replay: те же версии — stale, проекция не меняется.
stale_before=$(consumer /stats | python3 -c 'import json,sys; print(json.load(sys.stdin)["stale"])')
snapshot=$(projection_users)
replay >/dev/null
for _ in $(seq 1 30); do
  stale_now=$(consumer /stats | python3 -c 'import json,sys; print(json.load(sys.stdin)["stale"])')
  [ "$stale_now" -ge $((stale_before + n_users)) ] && break
  sleep 1
done
[ "$stale_now" -ge $((stale_before + n_users)) ] || fail "повторный replay: stale=$stale_now, ожидалось >= $((stale_before + n_users))"
[ "$(projection_users)" = "$snapshot" ] || fail "повторный replay изменил проекцию"
pass "повторный replay: $((stale_now - stale_before)) snapshot'ов отброшены как stale, проекция без изменений"

step "6. Вход через форму Keycloak (полный SAML-цикл)"
# curl отбрасывает Secure-куки Keycloak по http, а браузер на *.localhost
# их принимает (loopback — secure context). Helper воспроизводит именно
# браузерное поведение, поэтому проверяется настоящий путь пользователя.
SESSION=$(./scripts/stand-login.py "$HOST" "$USER_EMAIL" password /debug/echo 2>/tmp/stand-login.err) \
  || fail "вход не прошёл: $(cat /tmp/stand-login.err)"
pass "$(cat /tmp/stand-login.err)"
COOKIE=(-b "__identity_session=$SESSION")

step "7. Subject входа — стабильный id IdP, а не email"
subject=$(kubectl exec -n "$NS" "deploy/$RELEASE-identity-service-postgresql" -- \
  psql -U identity -d identity_development -tAc \
  "SELECT i.subject FROM user_identities i JOIN users u ON u.id = i.user_id WHERE u.email = '$USER_EMAIL' AND i.provider = 'okta'" 2>/dev/null | tr -d '[:space:]')
[ "$subject" = "$KC_USER_ID" ] || fail "привязка okta: subject=$subject, ожидался id из Keycloak $KC_USER_ID"
pass "привязка okta: subject = $subject = persistent NameID = SCIM externalId; email не участвует"

step "8. Заголовки контекста доходят до защищённого upstream"
echoed=$(curl -s "${RESOLVE[@]}" "${COOKIE[@]}" "http://$HOST/debug/echo")
for header in x-identity-user-id x-identity-email; do
  grep -qi "$header" <<< "$echoed" || fail "upstream не увидел $header"
done
uid=$(echo "$echoed" | tr ',' '\n' | grep -i 'x-identity-user-id' | head -1)
mail=$(echo "$echoed" | tr ',' '\n' | grep -i 'x-identity-email' | head -1)
pass "upstream получил: $uid $mail"

step "9. /auth/me через proxy"
me=$(curl -s "${RESOLVE[@]}" "${COOKIE[@]}" "http://$HOST/auth/me")
grep -q "$USER_EMAIL" <<< "$me" || fail "/auth/me вернул: $me"
pass "/auth/me -> $me"

step "10. Федерация: запрос через router в оба сабграфа"
fed=$(curl -s "${RESOLVE[@]}" "${COOKIE[@]}" -X POST "http://$HOST/graphql" \
  -H 'Content-Type: application/json' \
  -d '{"query":"{ orders { id seenIdentityHeaders owner { id email } } }"}')
grep -q '"x-identity-user-id=' <<< "$fed" || fail "сабграф не получил контекст: $fed"
grep -q "$USER_EMAIL" <<< "$fed" || fail "federation-ссылка owner -> User не разрешилась: $fed"
pass "router собрал ответ из двух сабграфов; стаб получил контекст, owner разрешён в identity"

step "11. Аноним не доходит до router"
code=$(curl -s -o /dev/null -w '%{http_code}' "${RESOLVE[@]}" -X POST "http://$HOST/graphql" \
  -H 'Content-Type: application/json' -d '{"query":"{ __schema { types { name } } }"}')
[ "$code" = 401 ] || [ "$code" = 403 ] || fail "анонимная introspection -> $code, ожидался отказ"
pass "анонимная introspection -> $code (схема не раскрывается)"

step "12. Первая установка чарта: хук миграций в пустом namespace"
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
trap 'probe_cleanup; kill $PF_PID 2>/dev/null; rm -f "$COOKIE_JAR"' EXIT
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
# Эталон — версия основной базы стенда: её мигрировал тот же образ, что
# запускает хук. Считать по файлам в чекауте нельзя: образ и ветка могут
# расходиться на одну миграцию.
expected=$(probe_psql identity_development "SELECT max(version_id) FROM goose_db_version")
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
trap 'kill $PF_PID 2>/dev/null; rm -f "$COOKIE_JAR"' EXIT

printf '\nСТЕНД ПРОВЕРЕН ЦЕЛИКОМ\n' 
