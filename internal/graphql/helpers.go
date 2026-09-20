package graphql

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/paderinandrey/identity-service/internal/access"
	"github.com/paderinandrey/identity-service/internal/graphql/model"
	"github.com/paderinandrey/identity-service/internal/identity"
	"github.com/paderinandrey/identity-service/internal/session"
)

// --- request-scoped prefetch of role assignments ---

// prefetch is the per-request cache a page resolver fills so that
// User.roles answers without one query per user. It lives behind a
// pointer in the context: child resolvers do not inherit a parent's
// context values, but they do see the shared struct.
type prefetch struct {
	mu          sync.Mutex
	assignments map[string][]access.Assignment
}

type prefetchKey struct{}

func withPrefetch(ctx context.Context) context.Context {
	return context.WithValue(ctx, prefetchKey{}, &prefetch{})
}

func prefetchFrom(ctx context.Context) *prefetch {
	p, _ := ctx.Value(prefetchKey{}).(*prefetch)
	return p
}

func (p *prefetch) put(assignments map[string][]access.Assignment, userIDs []string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.assignments == nil {
		p.assignments = map[string][]access.Assignment{}
	}
	// Every user of the page is recorded, including those without
	// assignments, so a later lookup is a hit rather than a fallback query.
	for _, id := range userIDs {
		p.assignments[id] = assignments[id]
	}
}

func (p *prefetch) get(userID string) ([]access.Assignment, bool) {
	if p == nil {
		return nil, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.assignments[userID]
	return a, ok
}

// --- keyset cursor ---

// encodeCursor makes the opaque cursor for a page position.
func encodeCursor(u *identity.User) string {
	raw, _ := json.Marshal(identity.PageKey{Name: u.Name, Email: u.Email, ID: u.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeCursor parses a cursor issued by encodeCursor.
func decodeCursor(cursor string) (*identity.PageKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, errors.New("malformed cursor")
	}
	var key identity.PageKey
	if err := json.Unmarshal(raw, &key); err != nil || key.ID == "" {
		return nil, errors.New("malformed cursor")
	}
	return &key, nil
}

func toModelAssignments(assignments []access.Assignment) []*model.RoleAssignment {
	out := make([]*model.RoleAssignment, len(assignments))
	for i, a := range assignments {
		out[i] = &model.RoleAssignment{
			Application: a.Application,
			Role:        a.Role,
			GrantedBy:   a.GrantedBy,
			GrantedAt:   a.GrantedAt,
		}
	}
	return out
}

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

// --- federation batch cap ---

// limitEntityBatches refuses an operation whose _entities fields carry,
// in total, more representations than maxEntityBatch, before any
// resolver runs. Representations arrive as a variable (routers) or as a
// literal list. The count is summed over the whole operation — aliases
// and fragments included — because each _entities field resolves its
// own batch, so per-field checks would let N aliases of a 200-item
// variable through (Codex review, PR #7).
func limitEntityBatches(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
	rc := graphql.GetOperationContext(ctx)
	if rc == nil || rc.Operation == nil {
		return next(ctx)
	}
	n := entityRepresentations(rc.Operation.SelectionSet, rc.Doc, rc.Variables, map[string]bool{})
	if n > maxEntityBatch {
		err := errWithCode(fmt.Sprintf("_entities batch of %d exceeds the limit of %d", n, maxEntityBatch), "BAD_USER_INPUT")
		return func(context.Context) *graphql.Response {
			return &graphql.Response{Errors: []*gqlerror.Error{err}}
		}
	}
	return next(ctx)
}

// entityRepresentations sums the representations of every _entities
// field reachable from set: direct, aliased, behind inline fragments or
// fragment spreads. seen guards against fragment cycles (the validator
// rejects them, but this runs on the parsed document either way).
func entityRepresentations(set ast.SelectionSet, doc *ast.QueryDocument, vars map[string]any, seen map[string]bool) int {
	n := 0
	for _, sel := range set {
		switch s := sel.(type) {
		case *ast.Field:
			if s.Name == "_entities" {
				n += representationCount(s, vars)
			}
		case *ast.InlineFragment:
			n += entityRepresentations(s.SelectionSet, doc, vars, seen)
		case *ast.FragmentSpread:
			if seen[s.Name] || doc == nil {
				continue
			}
			seen[s.Name] = true
			if def := doc.Fragments.ForName(s.Name); def != nil {
				n += entityRepresentations(def.SelectionSet, doc, vars, seen)
			}
		}
	}
	return n
}

func representationCount(field *ast.Field, vars map[string]any) int {
	arg := field.Arguments.ForName("representations")
	if arg == nil || arg.Value == nil {
		return 0
	}
	switch arg.Value.Kind {
	case ast.Variable:
		if list, ok := vars[arg.Value.Raw].([]any); ok {
			return len(list)
		}
	case ast.ListValue:
		return len(arg.Value.Children)
	}
	return 0
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
