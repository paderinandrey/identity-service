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

// Server-side caps against accidental full-directory dumps.
const (
	maxUsersPage = 200
	maxAuditPage = 100
)

// UserDirectory is the user lookup contract consumed by resolvers.
type UserDirectory interface {
	FindByID(ctx context.Context, id string) (*identity.User, error)
	SearchUsers(ctx context.Context, search string, includeInactive bool, limit int) ([]*identity.User, error)
}

// AccessDirectory is the access-control contract consumed by resolvers.
type AccessDirectory interface {
	GrantRole(ctx context.Context, actor, userID, app, role string) error
	RevokeRole(ctx context.Context, actor, userID, app, role string) error
	ListApplications(ctx context.Context) ([]access.Application, error)
	UserAssignments(ctx context.Context, userID string) ([]access.Assignment, error)
	AuditEntries(ctx context.Context, limit int) ([]access.AuditEntry, error)
}

// Resolver carries the dependencies of all GraphQL resolvers.
type Resolver struct {
	Directory UserDirectory
	Access    AccessDirectory
	Logger    *slog.Logger
}

// NewServer builds the /graphql handler: session-authenticated viewer,
// POST transport and introspection for authenticated callers.
func NewServer(resolver *Resolver, manager *session.Manager, users session.UserSource, logger *slog.Logger) http.Handler {
	srv := handler.New(generated.NewExecutableSchema(generated.Config{Resolvers: resolver}))
	srv.AddTransport(transport.POST{})
	srv.Use(extension.Introspection{})
	return authMiddleware(srv, manager, users, logger)
}
