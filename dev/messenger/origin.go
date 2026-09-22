package main

import (
	"context"
	"net/url"
	"strings"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Sessions are cookies, so a cross-site page can make the browser send a
// mutation with the victim's session; the gateway turns that session into
// trusted headers and the router forwards the browser's Origin. Every
// subgraph therefore guards its own mutations: an Origin that is present
// and not in the allowed set is refused before any resolver runs. No
// Origin means a non-browser caller and passes — the same rule as the
// identity subgraph.
func requireTrustedOrigin(allowed map[string]bool) graphql.OperationMiddleware {
	return func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		rc := graphql.GetOperationContext(ctx)
		if rc == nil || rc.Operation == nil || rc.Operation.Operation != ast.Mutation {
			return next(ctx)
		}
		if originAllowed(allowed, rc.Headers.Get("Origin")) {
			return next(ctx)
		}
		err := &gqlerror.Error{Message: "forbidden origin", Extensions: map[string]any{"code": "FORBIDDEN"}}
		return func(context.Context) *graphql.Response {
			return &graphql.Response{Errors: []*gqlerror.Error{err}}
		}
	}
}

// originAllowed: absent Origin passes; anything else is compared in
// canonical form against the set.
func originAllowed(allowed map[string]bool, origin string) bool {
	if origin == "" {
		return true
	}
	canonical, ok := canonicalOrigin(origin)
	return ok && allowed[canonical]
}

// originSet parses ALLOWED_ORIGINS (comma-separated URLs or origins),
// keeping only entries that canonicalize.
func originSet(list string) map[string]bool {
	set := map[string]bool{}
	for _, o := range strings.Split(list, ",") {
		if canonical, ok := canonicalOrigin(strings.TrimSpace(o)); ok {
			set[canonical] = true
		}
	}
	return set
}

// canonicalOrigin reduces a URL or Origin value to what a browser sends:
// lowercase scheme and host, no path, no default port — so a configured
// "https://App.example.com:443/base" and the browser's
// "https://app.example.com" compare equal. Same rule as the identity
// subgraph (Codex review, PR #16).
func canonicalOrigin(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host, true
}
