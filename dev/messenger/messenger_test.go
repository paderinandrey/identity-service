package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"

	"messenger/generated"
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

func newServer() http.Handler {
	srv := handler.New(generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{store: NewStore()}}))
	srv.AddTransport(transport.POST{})
	return withIdentity(srv)
}

func query(t *testing.T, h http.Handler, userID, perms, q string) gqlResp {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"query": q})
	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
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
	h := newServer()
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
