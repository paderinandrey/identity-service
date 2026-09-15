package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/paderinandrey/identity-service/internal/access"
	"github.com/paderinandrey/identity-service/internal/identity"
	"github.com/paderinandrey/identity-service/internal/postgres"
	"github.com/paderinandrey/identity-service/internal/session"
)

const (
	adminDBURL = "postgres://identity:identity@localhost:5433/identity_development?sslmode=disable"
	testDBName = "identity_graphql_test"
	testDBURL  = "postgres://identity:identity@localhost:5433/" + testDBName + "?sslmode=disable"
)

type env struct {
	server *httptest.Server
	pool   *pgxpool.Pool
	store  *postgres.Store
	access *postgres.AccessStore

	admin *identity.User // holds identity:access.manage
	alice *identity.User // active, no roles
	bob   *identity.User // deactivated
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
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+testDBName+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+testDBName); err != nil {
		t.Fatal(err)
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

	redisClient := redis.NewClient(&redis.Options{Addr: "localhost:6380", DB: 13})
	if err := redisClient.Ping(ctx).Err(); err != nil {
		t.Skipf("Redis from docker-compose is not available: %v (run `mise run up`)", err)
	}
	if err := redisClient.FlushDB(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = redisClient.Close() })

	store := postgres.NewStore(pool)
	accessStore := postgres.NewAccessStore(pool)

	seed := access.SeedConfig{Applications: []access.SeedApplication{
		{
			Name:        "identity",
			Permissions: []string{"access.manage"},
			Roles:       []access.SeedRole{{Name: "admin", Permissions: []string{"access.manage"}}},
		},
		{
			Name:        "gsh",
			Permissions: []string{"orders.read", "orders.write"},
			Roles:       []access.SeedRole{{Name: "sourcing_manager", Permissions: []string{"orders.read", "orders.write"}}},
		},
	}}
	if err := accessStore.Seed(ctx, "cli", seed); err != nil {
		t.Fatal(err)
	}

	e := &env{pool: pool, store: store, access: accessStore}
	e.admin = e.mustUser(t, "admin@example.com", "Ada Admin")
	e.alice = e.mustUser(t, "alice@example.com", "Alice Doe")
	e.bob = e.mustUser(t, "bob@example.com", "Bob Gone")
	if err := accessStore.GrantRole(ctx, "cli", e.admin.ID, "identity", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE users SET active = false WHERE id = $1", e.bob.ID); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := session.NewManager(redisClient, session.Config{
		CookieName:    "__identity_session_test",
		IdleTimeout:   time.Minute,
		Lifetime:      time.Hour,
		MaxConcurrent: 10,
	}, logger)
	users := testUserSource{store: store, access: accessStore}

	gql := NewServer(&Resolver{Directory: store, Access: accessStore, Logger: logger}, sessions, users, []string{"http://app.example.com"}, logger)

	mux := http.NewServeMux()
	mux.Handle("POST /graphql", gql)
	mux.HandleFunc("POST /test/login", func(w http.ResponseWriter, r *http.Request) {
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
	e.server = httptest.NewServer(sessions.Middleware(mux))
	t.Cleanup(e.server.Close)
	return e
}

func (e *env) mustUser(t *testing.T, email, name string) *identity.User {
	t.Helper()
	u, err := e.store.UpsertByEmail(t.Context(), email, name)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// testUserSource mirrors the production userSource without caches.
type testUserSource struct {
	store  *postgres.Store
	access *postgres.AccessStore
}

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

func (s testUserSource) Permissions(ctx context.Context, id string) ([]string, error) {
	return s.access.EffectivePermissions(ctx, id)
}

// client is an authenticated (or anonymous) GraphQL caller.
type client struct {
	http *http.Client
	url  string
}

func (e *env) login(t *testing.T, user *identity.User) *client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &client{http: &http.Client{Jar: jar}, url: e.server.URL}
	if user != nil {
		resp, err := c.http.Post(e.server.URL+"/test/login?user="+user.ID, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("login = %d", resp.StatusCode)
		}
	}
	return c
}

type gqlResponse struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []struct {
		Message    string            `json:"message"`
		Extensions map[string]string `json:"extensions"`
	} `json:"errors"`
}

func (c *client) query(t *testing.T, query string, vars map[string]any) gqlResponse {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	resp, err := c.http.Post(c.url+"/graphql", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out gqlResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

// queryFrom is query with a browser Origin header attached.
func (c *client) queryFrom(t *testing.T, origin, query string, vars map[string]any) gqlResponse {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, _ := http.NewRequest(http.MethodPost, c.url+"/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out gqlResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

func (r gqlResponse) errorCode() string {
	if len(r.Errors) == 0 {
		return ""
	}
	return r.Errors[0].Extensions["code"]
}

func mustUnmarshal[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return v
}

// --- tests ---

func TestUnauthenticatedRejected(t *testing.T) {
	e := newEnv(t)
	anon := e.login(t, nil)

	resp := anon.query(t, `{ me { user { id } } }`, nil)
	if resp.errorCode() != "UNAUTHENTICATED" {
		t.Fatalf("anonymous me: code = %q, errors = %v", resp.errorCode(), resp.Errors)
	}
	if len(resp.Data) != 0 {
		t.Errorf("anonymous request must carry no data, got %v", resp.Data)
	}

	introspect := anon.query(t, `{ __schema { queryType { name } } }`, nil)
	if introspect.errorCode() != "UNAUTHENTICATED" {
		t.Errorf("anonymous introspection: code = %q", introspect.errorCode())
	}
}

func TestMe(t *testing.T) {
	e := newEnv(t)
	c := e.login(t, e.admin)

	resp := c.query(t, `{ me { user { id email name } permissions } }`, nil)
	if len(resp.Errors) > 0 {
		t.Fatalf("me errors: %v", resp.Errors)
	}
	me := mustUnmarshal[struct {
		User        map[string]string `json:"user"`
		Permissions []string          `json:"permissions"`
	}](t, resp.Data["me"])
	if me.User["id"] != e.admin.ID || me.User["email"] != "admin@example.com" {
		t.Errorf("me.user = %v", me.User)
	}
	if len(me.Permissions) != 1 || me.Permissions[0] != ManagePermission {
		t.Errorf("me.permissions = %v", me.Permissions)
	}
}

func TestUsersSearch(t *testing.T) {
	e := newEnv(t)
	c := e.login(t, e.alice)

	resp := c.query(t, `query($s: String) { users(search: $s) { name } }`, map[string]any{"s": "ALICE"})
	users := mustUnmarshal[[]map[string]string](t, resp.Data["users"])
	if len(users) != 1 || users[0]["name"] != "Alice Doe" {
		t.Errorf("case-insensitive search = %v", users)
	}

	all := c.query(t, `{ users { email } }`, nil)
	for _, u := range mustUnmarshal[[]map[string]string](t, all.Data["users"]) {
		if u["email"] == "bob@example.com" {
			t.Error("inactive user leaked into default listing")
		}
	}

	withInactive := c.query(t, `{ users(includeInactive: true) { email active } }`, nil)
	found := false
	for _, u := range mustUnmarshal[[]map[string]any](t, withInactive.Data["users"]) {
		if u["email"] == "bob@example.com" && u["active"] == false {
			found = true
		}
	}
	if !found {
		t.Error("includeInactive must expose the deactivated user")
	}
}

func TestApplicationsDirectory(t *testing.T) {
	e := newEnv(t)
	c := e.login(t, e.alice)

	resp := c.query(t, `{ applications { name permissions roles { name permissions } } }`, nil)
	apps := mustUnmarshal[[]struct {
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
		Roles       []struct {
			Name        string   `json:"name"`
			Permissions []string `json:"permissions"`
		} `json:"roles"`
	}](t, resp.Data["applications"])
	if len(apps) != 2 {
		t.Fatalf("applications = %d, want 2", len(apps))
	}
	if apps[0].Name != "gsh" || apps[1].Name != "identity" {
		t.Errorf("order = %s, %s", apps[0].Name, apps[1].Name)
	}
	if len(apps[0].Roles) != 1 || len(apps[0].Roles[0].Permissions) != 2 {
		t.Errorf("gsh roles = %+v", apps[0].Roles)
	}
}

func TestGrantRevokeRole(t *testing.T) {
	e := newEnv(t)
	adminClient := e.login(t, e.admin)

	grant := adminClient.query(t,
		`mutation($u: ID!, $r: String!) { grantRole(userId: $u, role: $r) { id roles { application role grantedBy } } }`,
		map[string]any{"u": e.alice.ID, "r": "gsh/sourcing_manager"})
	if len(grant.Errors) > 0 {
		t.Fatalf("grantRole errors: %v", grant.Errors)
	}
	granted := mustUnmarshal[struct {
		Roles []map[string]string `json:"roles"`
	}](t, grant.Data["grantRole"])
	if len(granted.Roles) != 1 || granted.Roles[0]["grantedBy"] != e.admin.ID {
		t.Errorf("granted roles = %v, want grantedBy admin UUID", granted.Roles)
	}

	var actor string
	if err := e.pool.QueryRow(t.Context(),
		"SELECT actor FROM access_audit_log WHERE action = 'role.grant' AND target_user_id = $1", e.alice.ID).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if actor != e.admin.ID {
		t.Errorf("journal actor = %q, want admin UUID", actor)
	}

	revoke := adminClient.query(t,
		`mutation($u: ID!, $r: String!) { revokeRole(userId: $u, role: $r) { roles { role } } }`,
		map[string]any{"u": e.alice.ID, "r": "gsh/sourcing_manager"})
	revoked := mustUnmarshal[struct {
		Roles []map[string]string `json:"roles"`
	}](t, revoke.Data["revokeRole"])
	if len(revoked.Roles) != 0 {
		t.Errorf("roles after revoke = %v", revoked.Roles)
	}

	unknown := adminClient.query(t,
		`mutation($u: ID!) { grantRole(userId: $u, role: "gsh/ghost") { id } }`,
		map[string]any{"u": e.alice.ID})
	if unknown.errorCode() != "BAD_USER_INPUT" {
		t.Errorf("unknown role code = %q", unknown.errorCode())
	}
}

func TestGrantForbiddenWithoutPermission(t *testing.T) {
	e := newEnv(t)
	aliceClient := e.login(t, e.alice)

	var before int
	if err := e.pool.QueryRow(t.Context(), "SELECT count(*) FROM access_audit_log").Scan(&before); err != nil {
		t.Fatal(err)
	}
	resp := aliceClient.query(t,
		`mutation($u: ID!) { grantRole(userId: $u, role: "gsh/sourcing_manager") { id } }`,
		map[string]any{"u": e.alice.ID})
	if resp.errorCode() != "FORBIDDEN" {
		t.Fatalf("code = %q, want FORBIDDEN", resp.errorCode())
	}
	var after int
	if err := e.pool.QueryRow(t.Context(), "SELECT count(*) FROM access_audit_log").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Error("forbidden mutation must not touch the journal")
	}
	perms, err := e.access.EffectivePermissions(t.Context(), e.alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(perms) != 0 {
		t.Errorf("alice permissions = %v, want none", perms)
	}
}

func TestAccessAuditLog(t *testing.T) {
	e := newEnv(t)
	adminClient := e.login(t, e.admin)

	resp := adminClient.query(t, `{ accessAuditLog(limit: 10) { actor action createdAt } }`, nil)
	entries := mustUnmarshal[[]map[string]string](t, resp.Data["accessAuditLog"])
	if len(entries) < 2 {
		t.Fatalf("audit entries = %d, want seed composition + grant", len(entries))
	}
	for i := 1; i < len(entries); i++ {
		if entries[i-1]["createdAt"] < entries[i]["createdAt"] {
			t.Errorf("audit log must be newest-first")
		}
	}

	forbidden := e.login(t, e.alice).query(t, `{ accessAuditLog { action } }`, nil)
	if forbidden.errorCode() != "FORBIDDEN" {
		t.Errorf("audit without permission: code = %q", forbidden.errorCode())
	}
}

func TestFederation(t *testing.T) {
	e := newEnv(t)
	c := e.login(t, e.alice)

	sdl := c.query(t, `{ _service { sdl } }`, nil)
	sdlText := mustUnmarshal[map[string]string](t, sdl.Data["_service"])["sdl"]
	if !strings.Contains(sdlText, "@key") || !strings.Contains(sdlText, "type User") {
		t.Errorf("SDL misses federation entity, got: %.200s", sdlText)
	}

	entities := c.query(t,
		`query($reps: [_Any!]!) { _entities(representations: $reps) { ... on User { id email } } }`,
		map[string]any{"reps": []map[string]string{{"__typename": "User", "id": e.alice.ID}}})
	if len(entities.Errors) > 0 {
		t.Fatalf("_entities errors: %v", entities.Errors)
	}
	got := mustUnmarshal[[]map[string]string](t, entities.Data["_entities"])
	if len(got) != 1 || got[0]["email"] != "alice@example.com" {
		t.Errorf("_entities = %v", got)
	}

	missing := c.query(t,
		`query($reps: [_Any!]!) { _entities(representations: $reps) { ... on User { id } } }`,
		map[string]any{"reps": []map[string]string{{"__typename": "User", "id": "00000000-0000-0000-0000-000000000009"}}})
	if len(missing.Errors) == 0 {
		t.Error("unknown entity id must produce an error")
	}
}

func TestMain(m *testing.M) {
	fmt.Fprintln(os.Stderr, "graphql integration tests use docker-compose PostgreSQL/Redis (run `mise run up`)")
	os.Exit(m.Run())
}

func TestMutationRequiresTrustedOrigin(t *testing.T) {
	e := newEnv(t)
	admin := e.login(t, e.admin)
	grant := `mutation($u: ID!, $r: String!) { grantRole(userId: $u, role: $r) { id } }`
	vars := map[string]any{"u": e.alice.ID, "r": "gsh/sourcing_manager"}
	journal := func() int {
		var n int
		if err := e.pool.QueryRow(t.Context(), "SELECT count(*) FROM access_audit_log WHERE action = 'role.grant'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := journal()

	// A cross-site page carrying the admin's cookie.
	foreign := admin.queryFrom(t, "https://evil.example.com", grant, vars)
	if foreign.errorCode() != "FORBIDDEN" || foreign.Data["grantRole"] != nil {
		t.Fatalf("mutation from a foreign origin: code=%q data=%s; want FORBIDDEN and no data", foreign.errorCode(), foreign.Data["grantRole"])
	}
	if journal() != before {
		t.Error("a refused mutation must not reach the journal")
	}

	// Reads are not state changes: no Origin check.
	if resp := admin.queryFrom(t, "https://evil.example.com", `{ me { user { id } } }`, nil); resp.errorCode() != "" {
		t.Errorf("read from a foreign origin refused: %v", resp.Errors)
	}

	// The frontend's own origin, and a non-browser caller without Origin.
	if resp := admin.queryFrom(t, "http://app.example.com", grant, vars); resp.errorCode() != "" {
		t.Errorf("mutation from the trusted origin refused: %v", resp.Errors)
	}
	if resp := admin.query(t, grant, vars); resp.errorCode() != "" {
		t.Errorf("mutation without Origin refused: %v", resp.Errors)
	}
	if journal() != before+1 {
		t.Errorf("journal entries = %d, want %d (one grant, idempotent repeat)", journal(), before+1)
	}
}
