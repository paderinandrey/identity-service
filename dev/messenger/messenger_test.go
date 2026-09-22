package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthorizationRules(t *testing.T) {
	if err := requireSend(identityFromHeaders("", "")); err != errUnauthenticated {
		t.Errorf("no user: %v, want unauthenticated", err)
	}
	if err := requireSend(identityFromHeaders("u1", "gsh:orders.read")); err != errForbidden {
		t.Errorf("user without the permission: %v, want forbidden", err)
	}
	if err := requireSend(identityFromHeaders("u1", "gsh:orders.read, messenger:messages.send")); err != nil {
		t.Errorf("user with the permission: %v", err)
	}
	if err := requireUser(identityFromHeaders("u1", "")); err != nil {
		t.Errorf("inbox needs only a user: %v", err)
	}
}

type gqlResp struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []struct {
		Extensions map[string]string `json:"extensions"`
	} `json:"errors"`
}

func newTestServer() http.Handler {
	return withIdentity(newServer(NewStore(), originSet("http://app.example.com")))
}

func query(t *testing.T, h http.Handler, userID, perms, q string) gqlResp {
	t.Helper()
	return queryFrom(t, h, userID, perms, "", q)
}

func queryFrom(t *testing.T, h http.Handler, userID, perms, origin, q string) gqlResp {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"query": q})
	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if userID != "" {
		req.Header.Set("X-Identity-User-Id", userID)
	}
	req.Header.Set("X-Identity-Permissions", perms)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out gqlResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	return out
}

func code(r gqlResp) string {
	if len(r.Errors) == 0 {
		return ""
	}
	return r.Errors[0].Extensions["code"]
}

// The whole contract in one flow: a sender with the permission stores a
// message the recipient sees, a user without it is refused and nothing is
// stored, and a third user's inbox stays empty.
func TestSendAndInboxThroughTheSchema(t *testing.T) {
	h := newTestServer()
	send := `mutation { sendMessage(recipientId: "bob", text: "hi") { id author { id } recipient { id } } }`

	if got := code(query(t, h, "", "", send)); got != "UNAUTHENTICATED" {
		t.Errorf("anonymous send: code=%q", got)
	}
	if got := code(query(t, h, "mallory", "gsh:orders.read", send)); got != "FORBIDDEN" {
		t.Errorf("send without permission: code=%q", got)
	}
	sent := query(t, h, "ada", "messenger:messages.send,messenger:messages.read", send)
	if code(sent) != "" || !strings.Contains(string(sent.Data["sendMessage"]), `"author":{"id":"ada"}`) {
		t.Fatalf("send with permission: %+v %s", sent.Errors, sent.Data["sendMessage"])
	}

	inbox := `{ inbox { text author { id } } }`
	if got := string(query(t, h, "bob", "", inbox).Data["inbox"]); !strings.Contains(got, `"text":"hi"`) {
		t.Errorf("recipient inbox = %s", got)
	}
	if got := string(query(t, h, "carol", "", inbox).Data["inbox"]); got != "[]" {
		t.Errorf("unrelated inbox = %s, want empty (the refused message must not exist either)", got)
	}
	if got := code(query(t, h, "", "", inbox)); got != "UNAUTHENTICATED" {
		t.Errorf("anonymous inbox: code=%q", got)
	}
}

// A cross-site page carrying a valid session must not be able to send:
// the router forwards the browser's Origin, and mutations from anywhere
// but the configured origins are refused before the resolver runs.
// Reads and non-browser callers (no Origin) are left alone.
func TestOriginCanonicalization(t *testing.T) {
	allowed := originSet("https://App.example.com:443/base, http://app.example.com:80, ftp://x, bare")
	for origin, want := range map[string]bool{
		"":                                true, // non-browser caller
		"https://app.example.com":         true, // default port dropped, path dropped
		"HTTPS://APP.EXAMPLE.COM":         true, // case-insensitive
		"http://app.example.com":          true,
		"http://app.example.com:8080":     false, // explicit non-default port differs
		"https://evil.example.com":        false,
		"app.example.com":                 false, // not an origin
		"https://app.example.com.evil.io": false,
	} {
		if got := originAllowed(allowed, origin); got != want {
			t.Errorf("originAllowed(%q) = %v, want %v", origin, got, want)
		}
	}
	if len(allowed) != 2 {
		t.Errorf("allowed set = %v, want the two canonical http(s) origins only", allowed)
	}
}

func TestMutationsRequireTrustedOrigin(t *testing.T) {
	h := newTestServer()
	perms := "messenger:messages.send"
	send := `mutation { sendMessage(recipientId: "bob", text: "csrf") { id } }`
	if got := code(queryFrom(t, h, "ada", perms, "https://evil.example.com", send)); got != "FORBIDDEN" {
		t.Errorf("mutation from a foreign origin: code=%q, want FORBIDDEN", got)
	}
	if got := string(query(t, h, "bob", "", `{ inbox { text } }`).Data["inbox"]); got != "[]" {
		t.Errorf("refused mutation persisted a message: %s", got)
	}
	if got := code(queryFrom(t, h, "ada", perms, "http://app.example.com", send)); got != "" {
		t.Errorf("mutation from the trusted origin: code=%q", got)
	}
	if got := code(queryFrom(t, h, "ada", perms, "", send)); got != "" {
		t.Errorf("mutation without Origin (non-browser): code=%q", got)
	}
	if got := code(queryFrom(t, h, "bob", "", "https://evil.example.com", `{ inbox { text } }`)); got != "" {
		t.Errorf("read from a foreign origin refused: code=%q", got)
	}
}
