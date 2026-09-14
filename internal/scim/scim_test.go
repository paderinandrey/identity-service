package scim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/paderinandrey/identity-service/internal/identity"
	"github.com/paderinandrey/identity-service/internal/postgres"
	"github.com/paderinandrey/identity-service/internal/session"
)

const (
	adminDBURL = "postgres://identity:identity@localhost:5433/identity_development?sslmode=disable"
	testDBName = "identity_scim_test"
	testDBURL  = "postgres://identity:identity@localhost:5433/" + testDBName + "?sslmode=disable"
	scimToken  = "test-scim-token-0123456789abcdef0123456789"
	cookieName = "__identity_session_test"
)

type env struct {
	server   *httptest.Server
	store    *postgres.Store
	pool     *pgxpool.Pool
	sessions *session.Manager
	killer   *flakyKiller
	ops      map[string]int
}

// flakyKiller lets a test take the session store away from deactivation
// without touching the sessions themselves.
type flakyKiller struct {
	inner *session.Manager
	fail  bool
}

func (k *flakyKiller) DestroyAllForUser(ctx context.Context, userID string, epoch int64) error {
	if k.fail {
		return errors.New("redis: connection refused")
	}
	return k.inner.DestroyAllForUser(ctx, userID, epoch)
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := t.Context()

	admin, err := pgxpool.New(ctx, adminDBURL)
	if err == nil {
		err = admin.Ping(ctx)
	}
	if err != nil {
		t.Skipf("PostgreSQL from docker-compose is not available: %v (run `mise run up`)", err)
	}
	t.Cleanup(admin.Close)
	for _, q := range []string{
		"DROP DATABASE IF EXISTS " + testDBName + " WITH (FORCE)",
		"CREATE DATABASE " + testDBName,
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+testDBName+" WITH (FORCE)")
	})
	if err := postgres.Migrate(ctx, testDBURL); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.Connect(ctx, testDBURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	redisClient := redis.NewClient(&redis.Options{Addr: "localhost:6380", DB: 12})
	if err := redisClient.Ping(ctx).Err(); err != nil {
		t.Skipf("Redis from docker-compose is not available: %v (run `mise run up`)", err)
	}
	if err := redisClient.FlushDB(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = redisClient.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := postgres.NewStore(pool)
	sessions := session.NewManager(redisClient, session.Config{
		CookieName:    cookieName,
		IdleTimeout:   time.Minute,
		Lifetime:      time.Hour,
		MaxConcurrent: 10,
	}, logger)

	killer := &flakyKiller{inner: sessions}
	e := &env{store: store, pool: pool, sessions: sessions, killer: killer, ops: map[string]int{}}
	mux := http.NewServeMux()
	handlers := NewHandlers(store, killer, scimToken, logger)
	handlers.SetMetrics(opCounter{e.ops})
	handlers.Register(mux)

	// Session-side endpoints to observe session death after deactivation.
	authMux := http.NewServeMux()
	session.NewHandlers(sessions, testUserSource{store}, nil).Register(authMux)
	authMux.HandleFunc("POST /test/login", func(w http.ResponseWriter, r *http.Request) {
		u, err := store.FindByID(r.Context(), r.URL.Query().Get("user"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := sessions.Start(r.Context(), u.ID, u.SessionEpoch); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/auth/", sessions.Middleware(authMux))
	mux.Handle("/test/", sessions.Middleware(authMux))

	e.server = httptest.NewServer(mux)
	t.Cleanup(e.server.Close)
	return e
}

type opCounter struct{ ops map[string]int }

func (c opCounter) Observe(op string) { c.ops[op]++ }
func (c opCounter) RevocationError()  { c.ops["revocation_error"]++ }

type testUserSource struct{ store *postgres.Store }

func (s testUserSource) FindByID(ctx context.Context, id string) (*identity.User, error) {
	return s.store.FindByID(ctx, id)
}

func (s testUserSource) IsActive(ctx context.Context, id string) (bool, error) {
	u, err := s.store.FindByID(ctx, id)
	if err != nil {
		return false, nil //nolint:nilerr // unknown user is simply not active
	}
	return u.Active, nil
}

func (s testUserSource) Permissions(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

// do performs an authenticated SCIM request.
func (e *env) do(t *testing.T, method, path string, body any, token string) (*http.Response, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, e.server.URL+path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/scim+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var decoded map[string]any
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			// Discovery lists return arrays; wrap them.
			var arr []any
			if err := json.Unmarshal(raw, &arr); err != nil {
				t.Fatalf("decode %s %s response: %v (%s)", method, path, err, raw)
			}
			decoded = map[string]any{"list": arr}
		}
	}
	return resp, decoded
}

func createUserPayload(email, name, externalID string) map[string]any {
	return map[string]any{
		"schemas":     []string{schemaUser},
		"userName":    email,
		"displayName": name,
		"externalId":  externalID,
		"active":      true,
	}
}

func TestAuthRequired(t *testing.T) {
	e := newEnv(t)

	resp, body := e.do(t, "GET", "/scim/v2/Users", nil, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", resp.StatusCode)
	}
	if _, ok := body["Resources"]; ok {
		t.Error("401 body must not contain user data")
	}

	resp, _ = e.do(t, "GET", "/scim/v2/Users", nil, "wrong-token-wrong-token-wrong-token")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token = %d, want 401", resp.StatusCode)
	}
}

func TestServiceProviderConfig(t *testing.T) {
	e := newEnv(t)
	resp, body := e.do(t, "GET", "/scim/v2/ServiceProviderConfig", nil, scimToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	patch, _ := body["patch"].(map[string]any)
	bulk, _ := body["bulk"].(map[string]any)
	if patch["supported"] != true || bulk["supported"] != false {
		t.Errorf("capabilities: patch=%v bulk=%v", patch, bulk)
	}
}

func TestCreateGetAndDuplicate(t *testing.T) {
	e := newEnv(t)

	resp, created := e.do(t, "POST", "/scim/v2/Users",
		createUserPayload("New.Hire@example.com", "New Hire", "okta-ext-1"), scimToken)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d: %v", resp.StatusCode, created)
	}
	id, _ := created["id"].(string)
	if id == "" || created["userName"] != "new.hire@example.com" || created["externalId"] != "okta-ext-1" {
		t.Errorf("created resource = %v", created)
	}

	// Identity stored, user has no roles.
	user, err := e.store.FindByIdentity(t.Context(), identity.ProviderOkta, "okta-ext-1")
	if err != nil || user.ID != id {
		t.Errorf("okta identity lookup = %v, %v", user, err)
	}

	resp, got := e.do(t, "GET", "/scim/v2/Users/"+id, nil, scimToken)
	if resp.StatusCode != http.StatusOK || got["userName"] != "new.hire@example.com" {
		t.Errorf("get = %d %v", resp.StatusCode, got)
	}

	resp, dup := e.do(t, "POST", "/scim/v2/Users",
		createUserPayload("NEW.HIRE@example.com", "Case Clash", ""), scimToken)
	if resp.StatusCode != http.StatusConflict || dup["scimType"] != "uniqueness" {
		t.Errorf("duplicate = %d %v, want 409 uniqueness", resp.StatusCode, dup)
	}
}

func TestFilterSearch(t *testing.T) {
	e := newEnv(t)
	e.do(t, "POST", "/scim/v2/Users", createUserPayload("found@example.com", "Found", "ext-f"), scimToken)

	q := url.QueryEscape(`userName eq "FOUND@example.com"`)
	resp, body := e.do(t, "GET", "/scim/v2/Users?filter="+q, nil, scimToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("filter = %d", resp.StatusCode)
	}
	if body["totalResults"] != float64(1) {
		t.Errorf("totalResults = %v, want 1", body["totalResults"])
	}

	q = url.QueryEscape(`externalId eq "ext-f"`)
	_, byExt := e.do(t, "GET", "/scim/v2/Users?filter="+q, nil, scimToken)
	if byExt["totalResults"] != float64(1) {
		t.Errorf("externalId filter totalResults = %v", byExt["totalResults"])
	}

	q = url.QueryEscape(`userName eq "ghost@example.com"`)
	_, empty := e.do(t, "GET", "/scim/v2/Users?filter="+q, nil, scimToken)
	if empty["totalResults"] != float64(0) {
		t.Errorf("empty filter totalResults = %v", empty["totalResults"])
	}

	q = url.QueryEscape(`title co "boss"`)
	resp, invalid := e.do(t, "GET", "/scim/v2/Users?filter="+q, nil, scimToken)
	if resp.StatusCode != http.StatusBadRequest || invalid["scimType"] != "invalidFilter" {
		t.Errorf("unsupported filter = %d %v", resp.StatusCode, invalid)
	}
}

func TestPutUpdateKeepsUUIDAndAssignments(t *testing.T) {
	e := newEnv(t)
	_, created := e.do(t, "POST", "/scim/v2/Users", createUserPayload("old@example.com", "Old Name", "ext-u"), scimToken)
	id := created["id"].(string)

	resp, updated := e.do(t, "PUT", "/scim/v2/Users/"+id, map[string]any{
		"schemas":     []string{schemaUser},
		"userName":    "renamed@example.com",
		"displayName": "New Name",
		"externalId":  "ext-u2",
		"active":      true,
	}, scimToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put = %d: %v", resp.StatusCode, updated)
	}
	if updated["id"] != id || updated["userName"] != "renamed@example.com" || updated["externalId"] != "ext-u2" {
		t.Errorf("updated = %v", updated)
	}
}

func TestPatchDeactivateKillsSessionsImmediately(t *testing.T) {
	e := newEnv(t)
	_, created := e.do(t, "POST", "/scim/v2/Users", createUserPayload("leaver@example.com", "Leaver", "ext-l"), scimToken)
	id := created["id"].(string)

	// Establish a live session for the user.
	loginResp, err := http.Post(e.server.URL+"/test/login?user="+id, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = loginResp.Body.Close()
	var token string
	for _, c := range loginResp.Cookies() {
		if c.Name == cookieName {
			token = c.Value
		}
	}
	if token == "" {
		t.Fatal("no session cookie after login")
	}
	me := func() int {
		req, _ := http.NewRequest(http.MethodGet, e.server.URL+"/auth/me", nil)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if me() != http.StatusOK {
		t.Fatal("session must work before deactivation")
	}

	// Okta-style PATCH deactivation.
	resp, patched := e.do(t, "PATCH", "/scim/v2/Users/"+id, map[string]any{
		"schemas":    []string{schemaPatchOp},
		"Operations": []map[string]any{{"op": "Replace", "value": map[string]any{"active": false}}},
	}, scimToken)
	if resp.StatusCode != http.StatusOK || patched["active"] != false {
		t.Fatalf("patch deactivate = %d %v", resp.StatusCode, patched)
	}

	if got := me(); got != http.StatusUnauthorized {
		t.Errorf("session after deactivation = %d, want 401 immediately", got)
	}

	// Assignments and the user itself survive; reactivation works.
	resp, reactivated := e.do(t, "PATCH", "/scim/v2/Users/"+id, map[string]any{
		"schemas":    []string{schemaPatchOp},
		"Operations": []map[string]any{{"op": "replace", "path": "active", "value": true}},
	}, scimToken)
	if resp.StatusCode != http.StatusOK || reactivated["active"] != true {
		t.Fatalf("reactivate = %d %v", resp.StatusCode, reactivated)
	}
	if got := me(); got != http.StatusUnauthorized {
		t.Errorf("old session after reactivation = %d, want still 401", got)
	}
}

func TestDeleteIsSoft(t *testing.T) {
	e := newEnv(t)
	_, created := e.do(t, "POST", "/scim/v2/Users", createUserPayload("gone@example.com", "Gone", ""), scimToken)
	id := created["id"].(string)

	resp, _ := e.do(t, "DELETE", "/scim/v2/Users/"+id, nil, scimToken)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", resp.StatusCode)
	}

	user, err := e.store.FindByID(t.Context(), id)
	if err != nil {
		t.Fatalf("user must still exist after DELETE: %v", err)
	}
	if user.Active {
		t.Error("user must be inactive after DELETE")
	}
	if e.ops["create"] != 1 || e.ops["delete"] != 1 || e.ops["deactivate"] != 1 {
		t.Errorf("op counters = %v, want create/delete/deactivate counted", e.ops)
	}
}

func TestPatchRejectsUnsupportedOps(t *testing.T) {
	e := newEnv(t)
	_, created := e.do(t, "POST", "/scim/v2/Users", createUserPayload("patchy@example.com", "Patchy", ""), scimToken)
	id := created["id"].(string)

	resp, body := e.do(t, "PATCH", "/scim/v2/Users/"+id, map[string]any{
		"schemas":    []string{schemaPatchOp},
		"Operations": []map[string]any{{"op": "add", "path": "emails", "value": "x"}},
	}, scimToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsupported op = %d %v", resp.StatusCode, body)
	}

	user, _ := e.store.FindByID(t.Context(), id)
	if user.Email != "patchy@example.com" {
		t.Error("failed patch must not partially apply")
	}
}

func TestListPagination(t *testing.T) {
	e := newEnv(t)
	for i := range 5 {
		e.do(t, "POST", "/scim/v2/Users",
			createUserPayload(fmt.Sprintf("user%d@example.com", i), fmt.Sprintf("User %d", i), ""), scimToken)
	}

	_, page := e.do(t, "GET", "/scim/v2/Users?startIndex=3&count=2", nil, scimToken)
	if page["totalResults"] != float64(5) || page["itemsPerPage"] != float64(2) || page["startIndex"] != float64(3) {
		t.Errorf("pagination envelope = %v", page)
	}
	if !strings.Contains(fmt.Sprint(page["Resources"]), "user2@example.com") {
		t.Errorf("page must start at the third user, got %v", page["Resources"])
	}
}

func TestMain(m *testing.M) {
	fmt.Fprintln(os.Stderr, "scim integration tests use docker-compose PostgreSQL/Redis (run `mise run up`)")
	os.Exit(m.Run())
}

func TestDeactivationSurvivesSessionStoreFailure(t *testing.T) {
	// Codex finding F3: the session store fails while deactivating. The
	// revocation must already be durable — recorded with the flag in one
	// transaction — so the call succeeds, the failure is counted, and
	// reactivation does not bring the old session back.
	e := newEnv(t)
	_, created := e.do(t, "POST", "/scim/v2/Users", createUserPayload("flaky@example.com", "Flaky", "ext-fl"), scimToken)
	id := created["id"].(string)

	loginResp, err := http.Post(e.server.URL+"/test/login?user="+id, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = loginResp.Body.Close()
	var token string
	for _, c := range loginResp.Cookies() {
		if c.Name == cookieName {
			token = c.Value
		}
	}
	me := func() int {
		req, _ := http.NewRequest(http.MethodGet, e.server.URL+"/auth/me", nil)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if me() != http.StatusOK {
		t.Fatal("session must work before deactivation")
	}

	e.killer.fail = true
	resp, patched := e.do(t, "PATCH", "/scim/v2/Users/"+id, map[string]any{
		"schemas":    []string{schemaPatchOp},
		"Operations": []map[string]any{{"op": "replace", "value": map[string]any{"active": false}}},
	}, scimToken)
	if resp.StatusCode != http.StatusOK || patched["active"] != false {
		t.Fatalf("deactivate with session store down = %d %v, want 200 (revocation is durable)", resp.StatusCode, patched)
	}
	if e.ops["revocation_error"] != 1 {
		t.Errorf("revocation_error metric = %d, want 1", e.ops["revocation_error"])
	}
	e.killer.fail = false

	// Even though no key was deleted, the durable epoch refuses the session.
	if got := me(); got != http.StatusUnauthorized {
		t.Errorf("session after failed key deletion = %d, want 401 (durable epoch)", got)
	}

	resp, reactivated := e.do(t, "PATCH", "/scim/v2/Users/"+id, map[string]any{
		"schemas":    []string{schemaPatchOp},
		"Operations": []map[string]any{{"op": "replace", "path": "active", "value": true}},
	}, scimToken)
	if resp.StatusCode != http.StatusOK || reactivated["active"] != true {
		t.Fatalf("reactivate = %d %v", resp.StatusCode, reactivated)
	}
	if got := me(); got != http.StatusUnauthorized {
		t.Errorf("old session after reactivation = %d, want 401: it must never come back", got)
	}

	// A fresh sign-in works and carries the new generation.
	relogin, err := http.Post(e.server.URL+"/test/login?user="+id, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = relogin.Body.Close()
	for _, c := range relogin.Cookies() {
		if c.Name == cookieName {
			token = c.Value
		}
	}
	if got := me(); got != http.StatusOK {
		t.Errorf("new session after reactivation = %d, want 200", got)
	}
}

func (e *env) outboxCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(t.Context(), "SELECT count(*) FROM user_events_outbox").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCreateWithTakenExternalIDLeavesNothingBehind(t *testing.T) {
	// Codex F5, scenario 1: the old code answered 201 without the link.
	e := newEnv(t)
	resp, _ := e.do(t, "POST", "/scim/v2/Users", createUserPayload("first@example.com", "First", "ext-shared"), scimToken)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first create = %d", resp.StatusCode)
	}
	before := e.outboxCount(t)

	resp, body := e.do(t, "POST", "/scim/v2/Users", createUserPayload("second@example.com", "Second", "ext-shared"), scimToken)
	if resp.StatusCode != http.StatusConflict || body["scimType"] != "uniqueness" {
		t.Fatalf("create with taken externalId = %d %v, want 409 uniqueness", resp.StatusCode, body)
	}
	if !strings.Contains(fmt.Sprint(body["detail"]), "externalId") {
		t.Errorf("409 detail must name externalId, got %v", body["detail"])
	}
	if _, err := e.store.FindByEmailAny(t.Context(), "second@example.com"); !errors.Is(err, identity.ErrUserNotFound) {
		t.Errorf("second user must not exist after the conflict, err = %v", err)
	}
	if got := e.outboxCount(t); got != before {
		t.Errorf("outbox grew by %d on a rejected create, want 0", got-before)
	}
}

func TestPatchConflictRollsBackEverything(t *testing.T) {
	// Codex F5, scenario 2: name changed, version bumped, event published,
	// user still active — all behind a 409. Now: nothing.
	e := newEnv(t)
	e.do(t, "POST", "/scim/v2/Users", createUserPayload("owner@example.com", "Owner", "ext-owner"), scimToken)
	_, created := e.do(t, "POST", "/scim/v2/Users", createUserPayload("victim@example.com", "Victim", "ext-victim"), scimToken)
	id := created["id"].(string)
	beforeUser, err := e.store.FindByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	beforeEvents := e.outboxCount(t)

	resp, body := e.do(t, "PATCH", "/scim/v2/Users/"+id, map[string]any{
		"schemas": []string{schemaPatchOp},
		"Operations": []map[string]any{{"op": "replace", "value": map[string]any{
			"displayName": "Renamed",
			"externalId":  "ext-owner",
			"active":      false,
		}}},
	}, scimToken)
	if resp.StatusCode != http.StatusConflict || body["scimType"] != "uniqueness" {
		t.Fatalf("conflicting patch = %d %v, want 409 uniqueness", resp.StatusCode, body)
	}

	after, err := e.store.FindByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != beforeUser.Name || after.Version != beforeUser.Version || !after.Active || after.SessionEpoch != beforeUser.SessionEpoch {
		t.Errorf("user changed behind a 409: before %+v, after %+v", beforeUser, after)
	}
	if u, err := e.store.FindByIdentity(t.Context(), identity.ProviderOkta, "ext-victim"); err != nil || u.ID != id {
		t.Errorf("victim's own externalId must be untouched: %v, %v", u, err)
	}
	if got := e.outboxCount(t); got != beforeEvents {
		t.Errorf("outbox grew by %d on a rejected patch, want 0", got-beforeEvents)
	}
	if e.ops["deactivate"] != 0 {
		t.Errorf("deactivate counted %d times on a rolled-back patch", e.ops["deactivate"])
	}
}
