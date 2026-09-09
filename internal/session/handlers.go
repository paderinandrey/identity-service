package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/xometry-europe-gmbh/identity-service/internal/identity"
)

// Internal response headers consumed by the entry-point proxy.
const (
	HeaderUserID = "X-Identity-User-Id"
	HeaderEmail  = "X-Identity-Email"
	// HeaderPermissions carries effective permissions as comma-separated
	// "app:permission" values; present (possibly empty) on every 200.
	// Interim format until a signed internal token is chosen.
	HeaderPermissions = "X-Identity-Permissions"
)

// UserSource resolves users, their current active state and effective
// permissions.
type UserSource interface {
	FindByID(ctx context.Context, id string) (*identity.User, error)
	IsActive(ctx context.Context, id string) (bool, error)
	Permissions(ctx context.Context, id string) ([]string, error)
}

// Handlers exposes the HTTP endpoints owned by the session capability.
type Handlers struct {
	manager        *Manager
	users          UserSource
	allowedOrigins map[string]bool
}

// NewHandlers builds session endpoints. allowedOrigins lists origins
// permitted to call state-changing endpoints (base and frontend URLs).
func NewHandlers(manager *Manager, users UserSource, allowedOrigins []string) *Handlers {
	origins := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		if u, err := url.Parse(o); err == nil && u.Scheme != "" && u.Host != "" {
			origins[u.Scheme+"://"+u.Host] = true
		}
	}
	return &Handlers{manager: manager, users: users, allowedOrigins: origins}
}

// Register mounts session routes; the mux must be wrapped with Middleware.
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/me", h.handleMe)
	mux.HandleFunc("POST /auth/logout", h.handleLogout)
	mux.HandleFunc("GET /internal/session/validate", h.handleValidate)
}

// currentUser returns the active user of the request session, or nil.
func (h *Handlers) currentUser(r *http.Request) (*identity.User, error) {
	ctx := r.Context()
	userID := h.manager.UserID(ctx)
	if userID == "" {
		return nil, nil
	}
	active, err := h.users.IsActive(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, nil
	}
	user, err := h.users.FindByID(ctx, userID)
	if errors.Is(err, identity.ErrUserNotFound) {
		return nil, nil
	}
	return user, err
}

func (h *Handlers) handleMe(w http.ResponseWriter, r *http.Request) {
	user, err := h.currentUser(r)
	if err != nil {
		http.Error(w, "session validation failed", http.StatusServiceUnavailable)
		return
	}
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"id":    user.ID,
		"email": user.Email,
		"name":  user.Name,
	})
}

func (h *Handlers) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !h.originAllowed(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	if err := h.manager.Destroy(r.Context()); err != nil {
		http.Error(w, "logout failed", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) handleValidate(w http.ResponseWriter, r *http.Request) {
	user, err := h.currentUser(r)
	if err != nil {
		http.Error(w, "session validation failed", http.StatusServiceUnavailable)
		return
	}
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	perms, err := h.users.Permissions(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "session validation failed", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set(HeaderUserID, user.ID)
	w.Header().Set(HeaderEmail, user.Email)
	w.Header().Set(HeaderPermissions, strings.Join(perms, ","))
	w.WriteHeader(http.StatusOK)
}

// originAllowed accepts requests without an Origin header (non-browser
// clients) and browser requests whose Origin is explicitly allowed.
func (h *Handlers) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return h.allowedOrigins[origin]
}
