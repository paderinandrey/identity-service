package graphql

// This file will not be regenerated automatically.
//
// It serves as dependency injection for your app, add any dependencies you require
// here.

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"

	"github.com/paderinandrey/identity-service/internal/access"
	"github.com/paderinandrey/identity-service/internal/graphql/generated"
	"github.com/paderinandrey/identity-service/internal/identity"
	"github.com/paderinandrey/identity-service/internal/session"
)

// Server-side caps against accidental full-directory dumps and runaway
// queries.
const (
	maxUsersPage     = 200
	maxAuditPage     = 100
	maxBodyBytes     = 1 << 20 // same cap as SCIM and SAML
	complexityBudget = 2000
	// maxEntityBatch bounds _entities representations per operation: the
	// complexity budget prices the selection, not the list of keys, and
	// each key is a lookup (Codex review, PR #7).
	maxEntityBatch = maxUsersPage
	// applicationsWeight stands in for the directory size in complexity
	// arithmetic: a handful of applications with nested roles.
	applicationsWeight = 20
)

// UserDirectory is the user lookup contract consumed by resolvers.
type UserDirectory interface {
	FindByID(ctx context.Context, id string) (*identity.User, error)
	// SearchUsers returns up to limit users after the keyset position in
	// (name, email, id) order; nil after starts from the beginning.
	SearchUsers(ctx context.Context, search string, includeInactive bool, after *identity.PageKey, limit int) ([]*identity.User, error)
}

// AccessDirectory is the access-control contract consumed by resolvers.
type AccessDirectory interface {
	GrantRole(ctx context.Context, actor, userID, app, role string) error
	RevokeRole(ctx context.Context, actor, userID, app, role string) error
	ListApplications(ctx context.Context) ([]access.Application, error)
	UserAssignments(ctx context.Context, userID string) ([]access.Assignment, error)
	// AssignmentsForUsers loads many users' assignments in one query.
	AssignmentsForUsers(ctx context.Context, userIDs []string) (map[string][]access.Assignment, error)
	AuditEntries(ctx context.Context, limit int) ([]access.AuditEntry, error)
}

// Resolver carries the dependencies of all GraphQL resolvers.
type Resolver struct {
	Directory UserDirectory
	Access    AccessDirectory
	Logger    *slog.Logger
}

// NewServer builds the /graphql handler: session-authenticated viewer,
// POST transport, introspection for authenticated callers, a body cap,
// a complexity budget that counts list sizes and an Origin check on
// mutations. allowedOrigins lists the browser origins permitted to run
// mutations (base and frontend URLs); non-browser callers send no Origin.
func NewServer(resolver *Resolver, manager *session.Manager, users session.UserSource, allowedOrigins []string, logger *slog.Logger) http.Handler {
	cfg := generated.Config{Resolvers: resolver}
	// gqlgen's default costs a list as one element; without multipliers
	// a budget would not bound anything. Aliases are summed by gqlgen.
	cfg.Complexity.Query.Users = func(childComplexity int, _ *string, _ bool, first int, _ *string) int {
		return childComplexity * max(first, 1)
	}
	cfg.Complexity.Query.AccessAuditLog = func(childComplexity int, limit int) int {
		return childComplexity * max(limit, 1)
	}
	cfg.Complexity.Query.Applications = func(childComplexity int) int {
		return childComplexity * applicationsWeight
	}

	srv := handler.New(generated.NewExecutableSchema(cfg))
	srv.AddTransport(transport.POST{})
	srv.Use(extension.Introspection{})
	srv.Use(extension.FixedComplexityLimit(complexityBudget))
	srv.AroundOperations(requireTrustedOrigin(session.OriginSet(allowedOrigins)))
	srv.AroundOperations(limitEntityBatches)

	capped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		srv.ServeHTTP(w, r)
	})
	return authMiddleware(capped, manager, users, logger)
}
