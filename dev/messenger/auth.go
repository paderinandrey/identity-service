// Command messenger is a small federation subgraph on the local stand: it
// owns Message, references User from the identity subgraph, and — unlike
// the order stub — actually authorizes with the trusted context the
// router forwards from ext-auth. It exists to prove routing across three
// subgraphs and permission enforcement inside a service.
package main

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Identity is the trusted context ext-auth established and the router
// forwarded. Trusting these headers is only valid behind the gateway
// that overwrites them (NetworkPolicy in the identity chart keeps other
// pods out); this service never sees a browser directly.
type Identity struct {
	UserID      string
	Permissions map[string]bool
}

type identityKey struct{}

// withIdentity parses the x-identity-* headers into the request context.
func withIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{},
			identityFromHeaders(r.Header.Get("X-Identity-User-Id"), r.Header.Get("X-Identity-Permissions")))))
	})
}

func identityFromHeaders(userID, permissions string) Identity {
	id := Identity{UserID: strings.TrimSpace(userID), Permissions: map[string]bool{}}
	for _, p := range strings.Split(permissions, ",") {
		if p = strings.TrimSpace(p); p != "" {
			id.Permissions[p] = true
		}
	}
	return id
}

func identityFrom(ctx context.Context) Identity {
	if id, ok := ctx.Value(identityKey{}).(Identity); ok {
		return id
	}
	return Identity{Permissions: map[string]bool{}}
}

const permSend = "messenger:messages.send"

var (
	errUnauthenticated = errors.New("no user in the trusted context")
	errForbidden       = errors.New("permission " + permSend + " required")
)

// requireUser is the rule for reads: a user must be present.
func requireUser(id Identity) error {
	if id.UserID == "" {
		return errUnauthenticated
	}
	return nil
}

// requireSend is the rule for sending: a user with messages.send.
func requireSend(id Identity) error {
	if err := requireUser(id); err != nil {
		return err
	}
	if !id.Permissions[permSend] {
		return errForbidden
	}
	return nil
}

// gqlError maps the rule errors to GraphQL error codes clients branch on.
func gqlError(ctx context.Context, err error) error {
	code := "INTERNAL"
	switch {
	case errors.Is(err, errUnauthenticated):
		code = "UNAUTHENTICATED"
	case errors.Is(err, errForbidden):
		code = "FORBIDDEN"
	}
	return &gqlerror.Error{Path: graphql.GetPath(ctx), Message: err.Error(), Extensions: map[string]any{"code": code}}
}
