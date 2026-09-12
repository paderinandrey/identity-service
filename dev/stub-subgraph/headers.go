package main

import (
	"context"
	"net/http"
	"sort"
	"strings"
)

type headersKey struct{}

// withHeaders records the identity context headers the router passed on, so
// resolvers can report them back — that is the whole point of this stub.
func withHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen := []string{}
		for name, values := range r.Header {
			if strings.HasPrefix(strings.ToLower(name), "x-identity-") {
				seen = append(seen, strings.ToLower(name)+"="+strings.Join(values, ","))
			}
		}
		sort.Strings(seen)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), headersKey{}, seen)))
	})
}

func identityHeaders(ctx context.Context) []string {
	if v, ok := ctx.Value(headersKey{}).([]string); ok {
		return v
	}
	return []string{}
}

// ownerIDFromContext returns the user id the router forwarded, so the
// federated Order.owner reference points at a real user in the identity
// subgraph rather than an invented id.
func ownerIDFromContext(ctx context.Context) string {
	for _, h := range identityHeaders(ctx) {
		if strings.HasPrefix(h, "x-identity-user-id=") {
			return strings.TrimPrefix(h, "x-identity-user-id=")
		}
	}
	return ""
}
