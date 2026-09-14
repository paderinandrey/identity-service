package session

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/paderinandrey/identity-service/internal/identity"
)

// E2EUserLookup finds active users for the test-login endpoint.
type E2EUserLookup interface {
	FindActiveByEmail(ctx context.Context, email string) (*identity.User, error)
}

// RegisterE2ELogin mounts POST /internal/e2e/login: a machine-token
// protected endpoint that issues a regular session for an existing active
// user, so E2E suites can authenticate without driving the IdP UI.
// Deliberately leaves no sign-in traces: no last_sign_in_at update, no
// SSO metrics. The mux must be wrapped with the session Middleware.
func RegisterE2ELogin(mux *http.ServeMux, manager *Manager, users E2EUserLookup, token string, logger *slog.Logger) {
	tokenHash := sha256.Sum256([]byte(token))

	mux.HandleFunc("POST /internal/e2e/login", func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		digest := sha256.Sum256([]byte(raw))
		if !ok || subtle.ConstantTimeCompare(digest[:], tokenHash[:]) != 1 {
			logger.Warn("e2e login auth failure")
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}

		var payload struct {
			Email string `json:"email"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&payload); err != nil || payload.Email == "" {
			http.Error(w, "email is required", http.StatusBadRequest)
			return
		}

		user, err := users.FindActiveByEmail(r.Context(), identity.NormalizeEmail(payload.Email))
		if errors.Is(err, identity.ErrUserNotFound) {
			http.Error(w, "user not found or inactive", http.StatusNotFound)
			return
		}
		if err != nil {
			logger.Error("e2e login lookup failed", "error", err)
			http.Error(w, "lookup failed", http.StatusServiceUnavailable)
			return
		}

		if err := manager.Start(r.Context(), user.ID, user.SessionEpoch); err != nil {
			logger.Error("e2e login session start failed", "error", err)
			http.Error(w, "session start failed", http.StatusServiceUnavailable)
			return
		}
		logger.Info("e2e session issued", "user_id", user.ID)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id": user.ID, "email": user.Email})
	})
}
