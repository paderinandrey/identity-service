package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/paderinandrey/identity-service/internal/identity"
)

const cookieName = "__identity_session_test"

type fakeUsers struct {
	users    map[string]*identity.User
	inactive map[string]bool
	perms    map[string][]string
	failing  bool

	// Per-request lookup counts: validate runs on every ecosystem
	// request, so redundant lookups must not creep back in.
	findCalls  int
	activeCall int
	permCalls  int
}

func (f *fakeUsers) Permissions(_ context.Context, id string) ([]string, error) {
	f.permCalls++
	if f.failing {
		return nil, context.DeadlineExceeded
	}
	return f.perms[id], nil
}

func (f *fakeUsers) FindByID(_ context.Context, id string) (*identity.User, error) {
	f.findCalls++
	if u, ok := f.users[id]; ok {
		return u, nil
	}
	return nil, identity.ErrUserNotFound
}

func (f *fakeUsers) IsActive(_ context.Context, id string) (bool, error) {
	f.activeCall++
	if f.failing {
		return false, context.DeadlineExceeded
	}
	_, known := f.users[id]
	return known && !f.inactive[id], nil
}

func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "localhost:6380", DB: 15})
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Skipf("Redis from docker-compose is not available: %v (run `mise run up`)", err)
	}
	if err := client.FlushDB(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

type env struct {
	server  *httptest.Server
	client  *http.Client
	manager *Manager
	users   *fakeUsers
	mux     *http.ServeMux
	redis   *redis.Client
}

// newEnv wires Manager+Handlers with a test-only login endpoint.
func newEnv(t *testing.T, redisClient *redis.Client, cfg Config) *env {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := NewManager(redisClient, cfg, logger)
	users := &fakeUsers{
		users: map[string]*identity.User{
			"u1": {ID: "u1", Email: "u1@example.com", Name: "User One", Active: true},
		},
		inactive: map[string]bool{},
		perms:    map[string][]string{"u1": {"gsh:orders.read", "gsh:orders.write"}},
	}

	mux := http.NewServeMux()
	NewHandlers(manager, users, []string{"http://app.example.com"}).Register(mux)
	mux.HandleFunc("POST /test/login", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("user")
		var epoch int64
		if u, ok := users.users[id]; ok {
			epoch = u.SessionEpoch
		}
		if err := manager.Start(r.Context(), id, epoch); err != nil {
			if errors.Is(err, ErrUserRevoked) {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	server := httptest.NewServer(manager.Middleware(mux))
	t.Cleanup(server.Close)

	jar, _ := cookiejar.New(nil)
	return &env{
		server:  server,
		client:  &http.Client{Jar: jar},
		manager: manager,
		users:   users,
		mux:     mux,
		redis:   redisClient,
	}
}

// meWithToken presents a raw session token, the way a stolen or stale
// cookie would arrive, and returns the status of /auth/me.
func (e *env) meWithToken(t *testing.T, token string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.server.URL+"/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func defaultConfig() Config {
	return Config{
		CookieName:    cookieName,
		IdleTimeout:   time.Minute,
		Lifetime:      time.Hour,
		MaxConcurrent: 100,
	}
}

func (e *env) login(t *testing.T, user string) *http.Response { //nolint:unparam // single test user today
	t.Helper()
	resp, err := e.client.Post(e.server.URL+"/test/login?user="+user, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	return resp
}

func (e *env) sessionCookie(t *testing.T) *http.Cookie {
	t.Helper()
	u, _ := url.Parse(e.server.URL)
	for _, c := range e.client.Jar.Cookies(u) {
		if c.Name == cookieName {
			return c
		}
	}
	return nil
}

func (e *env) get(t *testing.T, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.server.URL+path, nil)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestLoginSetsCookieAndMeWorks(t *testing.T) {
	e := newEnv(t, testRedis(t), defaultConfig())

	resp := e.login(t, "u1")
	setCookie := resp.Header.Get("Set-Cookie")
	if !strings.Contains(setCookie, cookieName+"=") ||
		!strings.Contains(setCookie, "HttpOnly") ||
		!strings.Contains(setCookie, "SameSite=Lax") ||
		!strings.Contains(setCookie, "Path=/") {
		t.Errorf("Set-Cookie missing required attributes: %q", setCookie)
	}

	me := e.get(t, "/auth/me")
	if me.StatusCode != http.StatusOK {
		t.Fatalf("GET /auth/me = %d, want 200", me.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(me.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["id"] != "u1" || body["email"] != "u1@example.com" || body["name"] != "User One" {
		t.Errorf("me body = %v", body)
	}
}

func TestMeWithoutSession(t *testing.T) {
	e := newEnv(t, testRedis(t), defaultConfig())
	if resp := e.get(t, "/auth/me"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /auth/me without cookie = %d, want 401", resp.StatusCode)
	}
}

func TestTokenRotationOnRelogin(t *testing.T) {
	e := newEnv(t, testRedis(t), defaultConfig())

	e.login(t, "u1")
	first := e.sessionCookie(t).Value
	e.login(t, "u1")
	second := e.sessionCookie(t).Value
	if first == second {
		t.Fatal("session token must rotate on re-login")
	}

	// The old token must be dead server-side.
	req, _ := http.NewRequest(http.MethodGet, e.server.URL+"/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: first})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("old token after rotation = %d, want 401", resp.StatusCode)
	}
}

func TestIdleExpiry(t *testing.T) {
	cfg := defaultConfig()
	cfg.IdleTimeout = time.Second
	e := newEnv(t, testRedis(t), cfg)

	e.login(t, "u1")
	if resp := e.get(t, "/auth/me"); resp.StatusCode != http.StatusOK {
		t.Fatalf("fresh session: me = %d", resp.StatusCode)
	}
	time.Sleep(1300 * time.Millisecond)
	if resp := e.get(t, "/auth/me"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("idle-expired session: me = %d, want 401", resp.StatusCode)
	}
}

func TestConcurrentSessionLimitEvictsOldest(t *testing.T) {
	redisClient := testRedis(t)
	cfg := defaultConfig()
	cfg.MaxConcurrent = 2
	e := newEnv(t, redisClient, cfg)

	tokens := make([]string, 3)
	for i := range 3 {
		jar, _ := cookiejar.New(nil)
		e.client = &http.Client{Jar: jar}
		e.login(t, "u1")
		tokens[i] = e.sessionCookie(t).Value
		time.Sleep(10 * time.Millisecond) // distinct scores in the index
	}

	statuses := make([]int, 3)
	for i, token := range tokens {
		req, _ := http.NewRequest(http.MethodGet, e.server.URL+"/auth/me", nil)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		statuses[i] = resp.StatusCode
	}
	if statuses[0] != http.StatusUnauthorized {
		t.Errorf("oldest session = %d, want 401 (evicted)", statuses[0])
	}
	if statuses[1] != http.StatusOK || statuses[2] != http.StatusOK {
		t.Errorf("newer sessions = %v, want 200", statuses[1:])
	}
}

func TestLogout(t *testing.T) {
	e := newEnv(t, testRedis(t), defaultConfig())
	e.login(t, "u1")
	old := e.sessionCookie(t).Value

	req, _ := http.NewRequest(http.MethodPost, e.server.URL+"/auth/logout", nil)
	req.Header.Set("Origin", "http://app.example.com")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", resp.StatusCode)
	}

	req2, _ := http.NewRequest(http.MethodGet, e.server.URL+"/auth/me", nil)
	req2.AddCookie(&http.Cookie{Name: cookieName, Value: old})
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("me with destroyed token = %d, want 401", resp2.StatusCode)
	}
}

func TestLogoutRejectsForeignOrigin(t *testing.T) {
	e := newEnv(t, testRedis(t), defaultConfig())
	e.login(t, "u1")

	req, _ := http.NewRequest(http.MethodPost, e.server.URL+"/auth/logout", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin logout = %d, want 403", resp.StatusCode)
	}
	if me := e.get(t, "/auth/me"); me.StatusCode != http.StatusOK {
		t.Errorf("session must survive rejected logout, me = %d", me.StatusCode)
	}
}

func TestValidateEndpoint(t *testing.T) {
	e := newEnv(t, testRedis(t), defaultConfig())

	if resp := e.get(t, "/internal/session/validate"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("validate without cookie = %d, want 401", resp.StatusCode)
	}

	e.login(t, "u1")
	resp := e.get(t, "/internal/session/validate")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("validate = %d, want 200", resp.StatusCode)
	}
	if resp.Header.Get(HeaderUserID) != "u1" || resp.Header.Get(HeaderEmail) != "u1@example.com" {
		t.Errorf("validate headers = %q / %q", resp.Header.Get(HeaderUserID), resp.Header.Get(HeaderEmail))
	}
	if got := resp.Header.Get(HeaderPermissions); got != "gsh:orders.read,gsh:orders.write" {
		t.Errorf("permissions header = %q", got)
	}
}

func TestValidatePermissionsHeaderEmptyWithoutRoles(t *testing.T) {
	e := newEnv(t, testRedis(t), defaultConfig())
	e.users.perms["u1"] = nil
	e.login(t, "u1")

	resp := e.get(t, "/internal/session/validate")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("validate = %d, want 200", resp.StatusCode)
	}
	vals, present := resp.Header[http.CanonicalHeaderKey(HeaderPermissions)]
	if !present || len(vals) != 1 || vals[0] != "" {
		t.Errorf("permissions header must be present and empty, got %v (present=%t)", vals, present)
	}
}

func TestValidateDeactivatedUser(t *testing.T) {
	e := newEnv(t, testRedis(t), defaultConfig())
	e.login(t, "u1")
	e.users.inactive["u1"] = true

	if resp := e.get(t, "/internal/session/validate"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("validate for deactivated user = %d, want 401", resp.StatusCode)
	}
	if resp := e.get(t, "/auth/me"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("me for deactivated user = %d, want 401", resp.StatusCode)
	}
}

func TestValidateFailsClosedWhenRedisDown(t *testing.T) {
	e := newEnv(t, testRedis(t), defaultConfig())
	e.login(t, "u1")
	cookie := e.sessionCookie(t)

	// Rebuild the stack on a dead Redis: same cookie, broken store.
	dead := redis.NewClient(&redis.Options{Addr: "localhost:1", DialTimeout: 200 * time.Millisecond})
	deadEnv := newEnv(t, dead, defaultConfig())

	req, _ := http.NewRequest(http.MethodGet, deadEnv.server.URL+"/internal/session/validate", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("validate with Redis down = %d, want 503 (fail-close, not 200/401)", resp.StatusCode)
	}
}

func TestDestroyAllForUser(t *testing.T) {
	redisClient := testRedis(t)
	e := newEnv(t, redisClient, defaultConfig())

	tokens := make([]string, 3)
	for i := range 3 {
		jar, _ := cookiejar.New(nil)
		e.client = &http.Client{Jar: jar}
		e.login(t, "u1")
		tokens[i] = e.sessionCookie(t).Value
	}

	// The directory has not moved (fake users keep epoch 0): the Redis
	// fence alone must kill the sessions.
	if err := e.manager.DestroyAllForUser(t.Context(), "u1", 1); err != nil {
		t.Fatalf("DestroyAllForUser: %v", err)
	}

	for _, token := range tokens {
		req, _ := http.NewRequest(http.MethodGet, e.server.URL+"/auth/me", nil)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("session %q after DestroyAllForUser = %d, want 401", token[:8], resp.StatusCode)
		}
	}

	count, err := redisClient.ZCard(t.Context(), "user_sessions:u1").Result()
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("user session index has %d entries, want 0", count)
	}
}

func TestValidateLooksUpUserOnce(t *testing.T) {
	e := newEnv(t, testRedis(t), defaultConfig())
	e.login(t, "u1")
	e.users.findCalls, e.users.activeCall, e.users.permCalls = 0, 0, 0

	if resp := e.get(t, "/internal/session/validate"); resp.StatusCode != http.StatusOK {
		t.Fatalf("validate = %d", resp.StatusCode)
	}

	if e.users.findCalls != 1 || e.users.activeCall != 1 || e.users.permCalls != 1 {
		t.Errorf("lookups per validate: find=%d active=%d perms=%d, want 1/1/1 (no redundant round trips on the hot path)",
			e.users.findCalls, e.users.activeCall, e.users.permCalls)
	}
}

func TestValidateAnsweredForAnyMethod(t *testing.T) {
	e := newEnv(t, testRedis(t), defaultConfig())
	e.login(t, "u1")
	cookie := e.sessionCookie(t)

	// ext-auth replays the method of the original request (GraphQL is POST)
	// and treats the configured path as a prefix, appending the original
	// path — both shapes must answer identically.
	paths := []string{"/internal/session/validate", "/internal/session/validate/graphql"}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		for _, path := range paths {
			req, _ := http.NewRequest(method, e.server.URL+path, nil)
			req.AddCookie(cookie)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK || resp.Header.Get(HeaderUserID) != "u1" {
				t.Errorf("%s %s = %d, user header %q; want 200 with context",
					method, path, resp.StatusCode, resp.Header.Get(HeaderUserID))
			}

			anon, _ := http.NewRequest(method, e.server.URL+path, nil)
			resp2, err := http.DefaultClient.Do(anon)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp2.Body.Close()
			if resp2.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s without cookie = %d, want 401", method, path, resp2.StatusCode)
			}
		}
	}
}

func TestInFlightRequestCannotRestoreRevokedSession(t *testing.T) {
	// Codex finding F2: scs re-commits every loaded session under an idle
	// timeout, so a request that loaded the session before the revocation
	// used to write it straight back after. The fence in the store drops
	// that write.
	e := newEnv(t, testRedis(t), defaultConfig())
	e.login(t, "u1")
	token := e.sessionCookie(t).Value

	entered := make(chan struct{})
	release := make(chan struct{})
	e.mux.HandleFunc("GET /test/slow", func(w http.ResponseWriter, r *http.Request) {
		if e.manager.UserID(r.Context()) == "" {
			http.Error(w, "no session", http.StatusUnauthorized)
			return
		}
		close(entered)
		<-release
		w.WriteHeader(http.StatusOK)
	})

	done := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, e.server.URL+"/test/slow", nil)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- -1
			return
		}
		_ = resp.Body.Close()
		done <- resp.StatusCode
	}()

	<-entered
	if err := e.manager.DestroyAllForUser(t.Context(), "u1", 1); err != nil {
		t.Fatalf("DestroyAllForUser: %v", err)
	}
	close(release)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("in-flight request = %d, want 200 (it had a valid session when it started)", code)
	}

	if got := e.meWithToken(t, token); got != http.StatusUnauthorized {
		t.Errorf("token after in-flight commit = %d, want 401: the stale session was written back", got)
	}
	exists, err := e.redis.Exists(t.Context(), storePrefix+token).Result()
	if err != nil {
		t.Fatal(err)
	}
	if exists != 0 {
		t.Error("revoked session key must not exist after the in-flight request finished")
	}
}

func TestFenceAloneRejectsSessionWithoutDeletion(t *testing.T) {
	// The fence is what makes a session invalid; deleting keys is hygiene.
	// A session whose key still exists but whose epoch is behind the fence
	// is not loaded, and is removed on the way out.
	e := newEnv(t, testRedis(t), defaultConfig())
	e.login(t, "u1")
	token := e.sessionCookie(t).Value

	if err := e.redis.Set(t.Context(), userEpochKey+"u1", 7, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if got := e.meWithToken(t, token); got != http.StatusUnauthorized {
		t.Errorf("session behind the fence = %d, want 401", got)
	}
	exists, _ := e.redis.Exists(t.Context(), storePrefix+token).Result()
	if exists != 0 {
		t.Error("stale session must be deleted when it is refused")
	}
}

func TestDurableEpochRejectsSessionWhenFenceWasNotRaised(t *testing.T) {
	// Redis SET of the fence failed during deactivation: the database epoch
	// moved, the fence did not. The second line of defence — the epoch
	// from the user directory — still refuses the session.
	e := newEnv(t, testRedis(t), defaultConfig())
	e.login(t, "u1")
	token := e.sessionCookie(t).Value

	e.users.users["u1"].SessionEpoch = 1 // as SetActive(false) would do
	if got := e.meWithToken(t, token); got != http.StatusUnauthorized {
		t.Errorf("session with stale durable epoch = %d, want 401", got)
	}
	if got := e.meWithToken(t, token); got != http.StatusUnauthorized {
		t.Errorf("second presentation = %d, want 401 (session destroyed)", got)
	}
}

func TestSessionWithoutFenceStaysValidUntilRevocation(t *testing.T) {
	// Sessions from before epochs existed have no fence key and epoch 0:
	// they keep working, and die at the first revocation like any other.
	e := newEnv(t, testRedis(t), defaultConfig())
	e.login(t, "u1")
	token := e.sessionCookie(t).Value
	if err := e.redis.Del(t.Context(), userEpochKey+"u1").Err(); err != nil {
		t.Fatal(err)
	}

	if got := e.meWithToken(t, token); got != http.StatusOK {
		t.Fatalf("session without a fence = %d, want 200", got)
	}
	if err := e.manager.DestroyAllForUser(t.Context(), "u1", 1); err != nil {
		t.Fatal(err)
	}
	if got := e.meWithToken(t, token); got != http.StatusUnauthorized {
		t.Errorf("after first revocation = %d, want 401", got)
	}
}

func TestReloginAfterRevocationGetsNewEpoch(t *testing.T) {
	e := newEnv(t, testRedis(t), defaultConfig())
	e.login(t, "u1")
	old := e.sessionCookie(t).Value

	if err := e.manager.DestroyAllForUser(t.Context(), "u1", 1); err != nil {
		t.Fatal(err)
	}
	e.users.users["u1"].SessionEpoch = 1

	jar, _ := cookiejar.New(nil)
	e.client = &http.Client{Jar: jar}
	e.login(t, "u1")
	if got := e.get(t, "/auth/me"); got.StatusCode != http.StatusOK {
		t.Errorf("new session after revocation = %d, want 200", got.StatusCode)
	}
	if got := e.meWithToken(t, old); got != http.StatusUnauthorized {
		t.Errorf("old token after re-login = %d, want 401", got)
	}
}

func TestSignInCannotRewindFence(t *testing.T) {
	// Codex P1: a sign-in that read epoch N from the directory before a
	// concurrent deactivation published N+1 must not rewind the fence to N
	// and hand out a live session for a revoked user.
	e := newEnv(t, testRedis(t), defaultConfig())
	if err := e.manager.DestroyAllForUser(t.Context(), "u1", 1); err != nil {
		t.Fatal(err)
	}

	// The fake directory still says epoch 0: this sign-in lost the race.
	resp, err := e.client.Post(e.server.URL+"/test/login?user=u1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sign-in behind the fence = %d, want 401", resp.StatusCode)
	}
	fence, err := e.redis.Get(t.Context(), userEpochKey+"u1").Int64()
	if err != nil || fence != 1 {
		t.Errorf("fence after losing sign-in = %d, %v; want 1 (never rewound)", fence, err)
	}
	if c := e.sessionCookie(t); c != nil {
		if got := e.meWithToken(t, c.Value); got != http.StatusUnauthorized {
			t.Errorf("session issued by a losing sign-in = %d, want 401", got)
		}
	}
}

func TestDelayedRevocationCannotLowerFence(t *testing.T) {
	e := newEnv(t, testRedis(t), defaultConfig())
	if err := e.manager.DestroyAllForUser(t.Context(), "u1", 3); err != nil {
		t.Fatal(err)
	}
	// A revocation that was delayed on the wire carries an older epoch.
	if err := e.manager.DestroyAllForUser(t.Context(), "u1", 2); err != nil {
		t.Fatal(err)
	}
	fence, _ := e.redis.Get(t.Context(), userEpochKey+"u1").Int64()
	if fence != 3 {
		t.Errorf("fence after delayed lower revocation = %d, want 3", fence)
	}

	e.users.users["u1"].SessionEpoch = 3
	e.login(t, "u1")
	if got := e.get(t, "/auth/me"); got.StatusCode != http.StatusOK {
		t.Errorf("session at the current generation = %d, want 200", got.StatusCode)
	}
}
