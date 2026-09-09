package graphql

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/xometry-europe-gmbh/identity-service/internal/identity"
	"github.com/xometry-europe-gmbh/identity-service/internal/session"
)

// ManagePermission guards access-management mutations and the audit log.
const ManagePermission = "identity:access.manage"

// Viewer is the authenticated caller of a GraphQL request.
type Viewer struct {
	User        *identity.User
	Permissions []string
}

// Has reports whether the viewer holds the permission.
func (v *Viewer) Has(perm string) bool {
	for _, p := range v.Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

type viewerKey struct{}

// ViewerFrom returns the request viewer, or nil.
func ViewerFrom(ctx context.Context) *Viewer {
	v, _ := ctx.Value(viewerKey{}).(*Viewer)
	return v
}

func errWithCode(message, code string) *gqlerror.Error {
	return &gqlerror.Error{Message: message, Extensions: map[string]any{"code": code}}
}

// requireViewer returns the viewer or an UNAUTHENTICATED error.
func requireViewer(ctx context.Context) (*Viewer, error) {
	if v := ViewerFrom(ctx); v != nil {
		return v, nil
	}
	return nil, errWithCode("authentication required", "UNAUTHENTICATED")
}

// requireManage returns the viewer or an UNAUTHENTICATED/FORBIDDEN error.
func requireManage(ctx context.Context) (*Viewer, error) {
	v, err := requireViewer(ctx)
	if err != nil {
		return nil, err
	}
	if !v.Has(ManagePermission) {
		return nil, errWithCode("requires "+ManagePermission, "FORBIDDEN")
	}
	return v, nil
}

// authMiddleware resolves the session into a Viewer. Requests without a
// valid session are rejected before query execution, so neither data nor
// introspection leaks to unauthenticated callers.
func authMiddleware(next http.Handler, manager *session.Manager, users session.UserSource, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := manager.UserID(ctx)
		if userID == "" {
			writeGraphQLError(w, "authentication required", "UNAUTHENTICATED")
			return
		}
		active, err := users.IsActive(ctx, userID)
		if err != nil {
			logger.Error("viewer resolution failed", "error", err)
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		if !active {
			writeGraphQLError(w, "authentication required", "UNAUTHENTICATED")
			return
		}
		user, err := users.FindByID(ctx, userID)
		if err != nil {
			logger.Error("viewer resolution failed", "error", err)
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		perms, err := users.Permissions(ctx, userID)
		if err != nil {
			logger.Error("viewer resolution failed", "error", err)
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		ctx = context.WithValue(ctx, viewerKey{}, &Viewer{User: user, Permissions: perms})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeGraphQLError(w http.ResponseWriter, message, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": nil,
		"errors": []map[string]any{{
			"message":    message,
			"extensions": map[string]string{"code": code},
		}},
	})
}
