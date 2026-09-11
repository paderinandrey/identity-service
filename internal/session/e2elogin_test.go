package session

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	"github.com/paderinandrey/identity-service/internal/identity"
)

const e2eToken = "test-e2e-login-token-0123456789abcdef"

// e2eLookup spies on lookups to prove the endpoint has no other side
// effects (no sign-in touch, no metrics — those APIs are not even
// reachable from the handler).
type e2eLookup struct {
	users map[string]*identity.User
	calls []string
}

func (l *e2eLookup) FindActiveByEmail(_ context.Context, email string) (*identity.User, error) {
	l.calls = append(l.calls, email)
	if u, ok := l.users[email]; ok && u.Active {
		return u, nil
	}
	return nil, identity.ErrUserNotFound
}

func newE2EEnv(t *testing.T, withToken bool) (*env, *e2eLookup) {
	t.Helper()
	e := newEnv(t, testRedis(t), defaultConfig())
	lookup := &e2eLookup{users: map[string]*identity.User{
		"u1@example.com": e.users.users["u1"],
	}}
	if withToken {
		// Re-mount the server with the e2e endpoint registered.
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		mux := http.NewServeMux()
		NewHandlers(e.manager, e.users, nil).Register(mux)
		RegisterE2ELogin(mux, e.manager, lookup, e2eToken, logger)
		e.server.Close()
		e.server = httptest.NewServer(e.manager.Middleware(mux))
		t.Cleanup(e.server.Close)
		jar, _ := cookiejar.New(nil)
		e.client = &http.Client{Jar: jar}
	}
	return e, lookup
}

func (e *env) e2eLogin(t *testing.T, token, email string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email})
	req, _ := http.NewRequest(http.MethodPost, e.server.URL+"/internal/e2e/login", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestE2ELoginIssuesRegularSession(t *testing.T) {
	e, lookup := newE2EEnv(t, true)

	resp := e.e2eLogin(t, e2eToken, "  U1@Example.com ") // normalization
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("e2e login = %d", resp.StatusCode)
	}
	if c := e.sessionCookie(t); c == nil {
		t.Fatal("no session cookie issued")
	}
	me := e.get(t, "/auth/me")
	if me.StatusCode != http.StatusOK {
		t.Fatalf("me after e2e login = %d, want 200", me.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(me.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["email"] != "u1@example.com" {
		t.Errorf("me = %v", body)
	}
	if len(lookup.calls) != 1 || lookup.calls[0] != "u1@example.com" {
		t.Errorf("lookup calls = %v, want single normalized email (no other store side effects)", lookup.calls)
	}
}

func TestE2ELoginRejectsBadToken(t *testing.T) {
	e, _ := newE2EEnv(t, true)

	for _, token := range []string{"", "wrong-token-wrong-token-wrong-token"} {
		resp := e.e2eLogin(t, token, "u1@example.com")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("token %q: status = %d, want 401", token, resp.StatusCode)
		}
	}
	if c := e.sessionCookie(t); c != nil {
		t.Error("no session must be issued on auth failure")
	}
}

func TestE2ELoginUnknownOrInactiveUser(t *testing.T) {
	e, lookup := newE2EEnv(t, true)
	lookup.users["off@example.com"] = &identity.User{ID: "off", Email: "off@example.com", Active: false}

	for _, email := range []string{"ghost@example.com", "off@example.com"} {
		resp := e.e2eLogin(t, e2eToken, email)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", email, resp.StatusCode)
		}
	}
	if c := e.sessionCookie(t); c != nil {
		t.Error("no session must be issued for unknown/inactive users")
	}
}

func TestE2ELoginAbsentWithoutToken(t *testing.T) {
	e, _ := newE2EEnv(t, false) // endpoint not registered

	resp := e.e2eLogin(t, e2eToken, "u1@example.com")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("without configured token: status = %d, want 404 (route absent)", resp.StatusCode)
	}
}
