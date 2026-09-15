package graphql

import (
	"context"
	"errors"

	"github.com/99designs/gqlgen/graphql"
	"github.com/paderinandrey/identity-service/internal/access"
	"github.com/paderinandrey/identity-service/internal/graphql/model"
	"github.com/paderinandrey/identity-service/internal/identity"
	"github.com/paderinandrey/identity-service/internal/session"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Helpers for the generated resolvers live outside the *.resolvers.go
// files: gqlgen rewrites those on generate.

func (r *mutationResolver) changeRole(ctx context.Context, userID, roleRef string, grant bool) (*model.User, error) {
	viewer, err := requireManage(ctx)
	if err != nil {
		return nil, err
	}
	app, role, err := access.SplitRoleRef(roleRef)
	if err != nil {
		return nil, errWithCode(err.Error(), "BAD_USER_INPUT")
	}

	if grant {
		err = r.Access.GrantRole(ctx, viewer.User.ID, userID, app, role)
	} else {
		err = r.Access.RevokeRole(ctx, viewer.User.ID, userID, app, role)
	}
	switch {
	case errors.Is(err, access.ErrRoleNotFound):
		return nil, errWithCode(err.Error(), "BAD_USER_INPUT")
	case err != nil:
		r.Logger.Error("role change failed", "error", err)
		return nil, errWithCode("role change failed", "INTERNAL")
	}

	user, err := r.Directory.FindByID(ctx, userID)
	if errors.Is(err, identity.ErrUserNotFound) {
		return nil, errWithCode("user not found", "BAD_USER_INPUT")
	}
	if err != nil {
		r.Logger.Error("user lookup failed", "error", err)
		return nil, errWithCode("user lookup failed", "INTERNAL")
	}
	return toModelUser(user), nil
}

func toModelUser(u *identity.User) *model.User {
	return &model.User{
		ID:           u.ID,
		Email:        u.Email,
		Name:         u.Name,
		Active:       u.Active,
		LastSignInAt: u.LastSignInAt,
	}
}

// --- CSRF: trusted Origin for cookie-authenticated mutations ---

// requireTrustedOrigin refuses a mutation whose Origin header is present
// and not in the allowed set, before any resolver runs — the same rule
// logout applies. Reads change nothing and are left alone. The router in
// front of this subgraph must propagate the browser's Origin, or every
// request looks like a non-browser client.
func requireTrustedOrigin(allowed map[string]bool) graphql.OperationMiddleware {
	return func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		rc := graphql.GetOperationContext(ctx)
		if rc == nil || rc.Operation == nil || rc.Operation.Operation != ast.Mutation {
			return next(ctx)
		}
		if session.OriginAllowed(allowed, rc.Headers.Get("Origin")) {
			return next(ctx)
		}
		err := errWithCode("forbidden origin", "FORBIDDEN")
		return func(context.Context) *graphql.Response {
			return &graphql.Response{Errors: []*gqlerror.Error{err}}
		}
	}
}
