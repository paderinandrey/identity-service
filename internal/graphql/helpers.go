package graphql

import (
	"context"
	"errors"

	"github.com/paderinandrey/identity-service/internal/access"
	"github.com/paderinandrey/identity-service/internal/graphql/model"
	"github.com/paderinandrey/identity-service/internal/identity"
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
