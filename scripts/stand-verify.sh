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
# Проекция живёт в памяти: рестарт консьюмера даёт пустую проекцию,
# без него replay проверялся бы поверх уже принятых живых событий.
kubectl rollout restart "deploy/$RELEASE-identity-service-stub-consumer" -n "$NS" >/dev/null
kubectl rollout status "deploy/$RELEASE-identity-service-stub-consumer" -n "$NS" --timeout=120s >/dev/null
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
[ -z "$(projection_users)" ] || fail "проекция не пуста после рестарта консьюмера: $(projection_users | tr '\n' ' ')"
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

# Мусор уходит в DLQ консьюмера, а не исчезает: публикуем в exchange тело,
# которое не разобрать, и ждём его в очереди .dlq.
dlq_depth() {
  kubectl exec -n "$NS" "deploy/$RELEASE-identity-service-rabbitmq" -- rabbitmqctl list_queues name messages 2>/dev/null \
    | awk '$1=="stub-consumer.users.dlq"{print $2}'
}
# Относительно текущей глубины: DLQ durable, и предыдущий прогон уже
# оставил в ней сообщение.
dlq_before=$(dlq_depth); dlq_before=${dlq_before:-0}
rejected_before=$(consumer /stats | python3 -c 'import json,sys; print(json.load(sys.stdin)["rejected"])')
kubectl exec -n "$NS" "deploy/$RELEASE-identity-service-rabbitmq" -- rabbitmqadmin --non-interactive \
  --username identity --password identity publish message \
  --exchange identity.events --routing-key user.updated --payload '{"not":"an event"}' >/dev/null 2>&1 \
  || fail "не удалось опубликовать тестовое сообщение через rabbitmqadmin"
for _ in $(seq 1 20); do
  dlq=$(dlq_depth); dlq=${dlq:-0}
  rejected_now=$(consumer /stats | python3 -c 'import json,sys; print(json.load(sys.stdin)["rejected"])')
  [ "$dlq" -gt "$dlq_before" ] && [ "$rejected_now" -gt "$rejected_before" ] && break
  sleep 1
done
[ "$dlq" -gt "$dlq_before" ] || fail "неразбираемое сообщение не попало в DLQ (глубина $dlq, была $dlq_before)"
[ "$rejected_now" = "$((rejected_before + 1))" ] || fail "rejected=$rejected_now, ожидалось $((rejected_before + 1))"
pass "неразбираемое тело: rejected +1, DLQ stub-consumer.users.dlq выросла с $dlq_before до $dlq"

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

# Два пода в namespace стенда: с метками разрешённого источника и без
# меток — как любой чужой под. Долгоживущие, а не `kubectl run -i`: CNI
# добавляет адрес нового пода в набор политики асинхронно, одноразовый
# под стреляет раньше и получает отказ при верных метках; после Ready
# адрес учтён. Печатают HTTP-код, 000 — соединения нет.
PROBE_ALLOWED="probe-allowed-$RANDOM"
PROBE_PLAIN="probe-plain-$RANDOM"
probe_cleanup_pods() { kubectl delete pod -n "$NS" "$PROBE_ALLOWED" "$PROBE_PLAIN" --ignore-not-found --wait=false >/dev/null 2>&1 || true; }
trap 'probe_cleanup_pods; kill $PF_PID 2>/dev/null; rm -f "$COOKIE_JAR"' EXIT
kubectl run "$PROBE_ALLOWED" -n "$NS" --restart=Never --image=curlimages/curl:latest \
  --labels=identity-stand/tools=true,app.kubernetes.io/name=graphql-router --command -- sleep 600 >/dev/null
kubectl run "$PROBE_PLAIN" -n "$NS" --restart=Never --image=curlimages/curl:latest --command -- sleep 600 >/dev/null
kubectl wait pod/"$PROBE_ALLOWED" pod/"$PROBE_PLAIN" -n "$NS" --for=condition=Ready --timeout=60s >/dev/null
probe_curl() { kubectl exec -n "$NS" "$1" -- curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$2" 2>/dev/null || true; }
SVC="http://$RELEASE-identity-service"

step "12. Внутренняя зона отсутствует на публичном порту"
code=$(probe_curl "$PROBE_ALLOWED" "$SVC:8080/internal/session/validate")
[ "$code" = 404 ] || fail "validate на :8080 -> $code, ожидалось 404 (зона не смонтирована)"
code_m=$(probe_curl "$PROBE_ALLOWED" "$SVC:8080/internal/metrics")
[ "$code_m" = 404 ] || fail "metrics на :8080 -> $code_m, ожидалось 404"
pass "validate и metrics на публичном порту -> 404"

step "13. Внутренний порт закрыт сетевой политикой"
code=$(probe_curl "$PROBE_PLAIN" "$SVC:8081/internal/session/validate")
[ "$code" = 000 ] || fail "validate на :8081 из немаркированного пода -> $code, ожидалось отсутствие соединения"
code=$(probe_curl "$PROBE_PLAIN" "$SVC:8080/healthz")
[ "$code" = 000 ] || fail "публичный порт из немаркированного пода -> $code, ожидалось отсутствие соединения"
code=$(probe_curl "$PROBE_ALLOWED" "$SVC:8081/internal/session/validate")
[ "$code" = 401 ] || fail "validate на :8081 из разрешённого пода без cookie -> $code, ожидалось 401"
code=$(probe_curl "$PROBE_ALLOWED" "$SVC:8081/internal/metrics")
[ "$code" = 200 ] || fail "metrics на :8081 из разрешённого пода -> $code, ожидалось 200"
pass "чужой под: соединения нет на оба порта; разрешённый под: validate 401 без cookie, metrics 200"

step "14. Подделанные заголовки контекста перезаписываются на proxy"
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

step "15. Третий сабграф: messenger через router, права из заголовка"
# stand-qa носит messenger/member; его cookie берётся e2e-входом с
# внутреннего порта из разрешённого пода. Значение cookie не печатается.
E2E_TOKEN="${E2E_TOKEN:-local-dev-e2e-token-0123456789abcdef}"
sq_cookie=$(kubectl exec -n "$NS" "$PROBE_ALLOWED" -- sh -c "curl -s -D - -o /dev/null -X POST $SVC:8081/internal/e2e/login \
  -H 'Authorization: Bearer $E2E_TOKEN' -d '{\"email\":\"stand-qa@example.com\"}' \
  | grep -i '^set-cookie' | sed 's/.*__identity_session=\([^;]*\).*/\1/'" 2>/dev/null)
[ -n "$sq_cookie" ] || fail "e2e-вход для stand-qa не выдал cookie"
qa_id=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<< "$me")
gql() { curl -s "${RESOLVE[@]}" -X POST "http://$HOST/graphql" -H 'Content-Type: application/json' -H "Origin: http://$HOST" "$@"; }
sent=$(gql -b "__identity_session=$sq_cookie" \
  -d "{\"query\":\"mutation { sendMessage(recipientId: \\\"$qa_id\\\", text: \\\"hello from the stand\\\") { id author { email } recipient { email } } }\"}")
grep -q '"author":{"email":"stand-qa@example.com"}' <<< "$sent" || fail "sendMessage: автор не разрешён через identity: $sent"
grep -q "\"recipient\":{\"email\":\"$USER_EMAIL\"}" <<< "$sent" || fail "sendMessage: получатель не разрешён: $sent"
denied=$(gql "${COOKIE[@]}" \
  -d "{\"query\":\"mutation { sendMessage(recipientId: \\\"$qa_id\\\", text: \\\"forbidden\\\") { id } }\"}")
grep -q 'FORBIDDEN' <<< "$denied" || fail "qa без роли смог отправить сообщение: $denied"
inbox=$(gql "${COOKIE[@]}" -d '{"query":"{ inbox { text author { email } } }"}')
grep -q '"text":"hello from the stand"' <<< "$inbox" || fail "inbox qa не содержит сообщения: $inbox"
grep -q 'forbidden' <<< "$inbox" && fail "отклонённое сообщение попало в inbox: $inbox"
pass "три сабграфа: messenger сохранил сообщение, identity разрешил автора и получателя; без права — FORBIDDEN; inbox qa получил его"
# messenger верит x-identity-* — значит к нему напрямую можно только из
# router (probe-под с меткой graphql-router его изображает), чужой под с
# подделанными заголовками соединения не получает.
MSG="http://$RELEASE-identity-service-messenger:8080/graphql"
forge=(-X POST -H 'Content-Type: application/json' -H "x-identity-user-id: $qa_id" -H 'x-identity-permissions: messenger:messages.send' -d '{"query":"{ inbox { id } }"}')
code=$(kubectl exec -n "$NS" "$PROBE_PLAIN" -- curl -s -o /dev/null -w '%{http_code}' --max-time 5 "${forge[@]}" "$MSG" 2>/dev/null || true)
[ "$code" = 000 ] || fail "чужой под достучался до messenger с подделанными заголовками -> $code"
code=$(kubectl exec -n "$NS" "$PROBE_ALLOWED" -- curl -s -o /dev/null -w '%{http_code}' --max-time 5 "${forge[@]}" "$MSG" 2>/dev/null || true)
[ "$code" = 200 ] || fail "router не достучался до messenger -> $code"
pass "messenger закрыт сетевой политикой: чужой под — соединения нет, router — 200"

step "16. Первая установка чарта: хук миграций в пустом namespace"
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
trap 'probe_cleanup; probe_cleanup_pods; kill $PF_PID 2>/dev/null; rm -f "$COOKIE_JAR"' EXIT
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
trap 'probe_cleanup_pods; kill $PF_PID 2>/dev/null; rm -f "$COOKIE_JAR"' EXIT

printf '\nСТЕНД ПРОВЕРЕН ЦЕЛИКОМ\n' 
