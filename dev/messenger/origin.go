package main

import (
	"context"
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

// originAllowed: absent Origin passes, anything else must match exactly
// (scheme, host, port) after lower-casing.
func originAllowed(allowed map[string]bool, origin string) bool {
	if origin == "" {
		return true
	}
	return allowed[strings.ToLower(strings.TrimRight(origin, "/"))]
}

// originSet parses ALLOWED_ORIGINS (comma-separated).
func originSet(list string) map[string]bool {
	set := map[string]bool{}
	for _, o := range strings.Split(list, ",") {
		if o = strings.ToLower(strings.TrimSpace(strings.TrimRight(o, "/"))); o != "" {
			set[o] = true
		}
	}
	return set
}
