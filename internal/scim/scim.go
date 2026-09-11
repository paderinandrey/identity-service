// Package scim implements the SCIM 2.0 subset used by the Okta
// provisioning client: Users CRUD with PATCH, eq-filters and discovery.
// Behavior mirrors the proven GSH implementation: soft deactivation kills
// sessions immediately, users are never physically deleted.
package scim

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xometry-europe-gmbh/identity-service/internal/identity"
)

// SCIM schema URNs.
const (
	schemaUser         = "urn:ietf:params:scim:schemas:core:2.0:User"
	schemaListResponse = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	schemaPatchOp      = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	schemaError        = "urn:ietf:params:scim:api:messages:2.0:Error"
)

const (
	contentType = "application/scim+json"
	maxPageSize = 200
	maxBodySize = 1 << 20 // 1 MiB
)

// UserStore is the persistence contract consumed by SCIM handlers.
type UserStore interface {
	FindByID(ctx context.Context, id string) (*identity.User, error)
	FindByEmailAny(ctx context.Context, email string) (*identity.User, error)
	ListUsersPage(ctx context.Context, offset, limit int) ([]*identity.User, int, error)
	CreateUser(ctx context.Context, email, name string, active bool) (*identity.User, error)
	UpdateUser(ctx context.Context, id, email, name string) (*identity.User, error)
	SetActive(ctx context.Context, id string, active bool) error
	FindByIdentity(ctx context.Context, provider, subject string) (*identity.User, error)
	ReplaceIdentity(ctx context.Context, userID, provider, subject string) error
	IdentitySubject(ctx context.Context, userID, provider string) (string, error)
}

// OpMetrics counts provisioning operations; nil disables instrumentation.
type OpMetrics interface {
	Observe(op string)
}

// SessionKiller revokes all sessions of a user on deactivation.
type SessionKiller interface {
	DestroyAllForUser(ctx context.Context, userID string) error
}

// Handlers serves the SCIM endpoints.
type Handlers struct {
	store     UserStore
	sessions  SessionKiller
	tokenHash [32]byte
	logger    *slog.Logger
	metrics   OpMetrics
}

// NewHandlers builds SCIM handlers guarded by the bearer token.
func NewHandlers(store UserStore, sessions SessionKiller, token string, logger *slog.Logger) *Handlers {
	return &Handlers{
		store:     store,
		sessions:  sessions,
		tokenHash: sha256.Sum256([]byte(token)),
		logger:    logger,
	}
}

// SetMetrics attaches operation instrumentation.
func (h *Handlers) SetMetrics(m OpMetrics) { h.metrics = m }

func (h *Handlers) countOp(op string) {
	if h.metrics != nil {
		h.metrics.Observe(op)
	}
}

// Register mounts SCIM routes on the mux (outside session middleware).
func (h *Handlers) Register(mux *http.ServeMux) {
	auth := h.requireToken
	mux.HandleFunc("GET /scim/v2/ServiceProviderConfig", auth(h.handleServiceProviderConfig))
	mux.HandleFunc("GET /scim/v2/ResourceTypes", auth(h.handleResourceTypes))
	mux.HandleFunc("GET /scim/v2/Schemas", auth(h.handleSchemas))
	mux.HandleFunc("GET /scim/v2/Users", auth(h.handleList))
	mux.HandleFunc("POST /scim/v2/Users", auth(h.handleCreate))
	mux.HandleFunc("GET /scim/v2/Users/{id}", auth(h.handleGet))
	mux.HandleFunc("PUT /scim/v2/Users/{id}", auth(h.handleReplace))
	mux.HandleFunc("PATCH /scim/v2/Users/{id}", auth(h.handlePatch))
	mux.HandleFunc("DELETE /scim/v2/Users/{id}", auth(h.handleDelete))
}

func (h *Handlers) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			h.logger.Warn("scim auth failure", "reason", "missing_token", "path", r.URL.Path)
			writeError(w, http.StatusUnauthorized, "", "authentication required")
			return
		}
		digest := sha256.Sum256([]byte(raw))
		if subtle.ConstantTimeCompare(digest[:], h.tokenHash[:]) != 1 {
			h.logger.Warn("scim auth failure", "reason", "invalid_token", "path", r.URL.Path)
			writeError(w, http.StatusUnauthorized, "", "authentication required")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
		next(w, r)
	}
}

// --- resource shapes ---

type userResource struct {
	Schemas     []string  `json:"schemas"`
	ID          string    `json:"id"`
	ExternalID  string    `json:"externalId,omitempty"`
	UserName    string    `json:"userName"`
	DisplayName string    `json:"displayName"`
	Active      bool      `json:"active"`
	Meta        *userMeta `json:"meta,omitempty"`
}

type userMeta struct {
	ResourceType string    `json:"resourceType"`
	Created      time.Time `json:"created"`
	LastModified time.Time `json:"lastModified"`
	Location     string    `json:"location"`
}

type userPayload struct {
	ExternalID  string `json:"externalId"`
	UserName    string `json:"userName"`
	DisplayName string `json:"displayName"`
	Name        struct {
		GivenName  string `json:"givenName"`
		FamilyName string `json:"familyName"`
	} `json:"name"`
	Active *bool `json:"active"`
}

func (p userPayload) displayNameOrFallback() string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	if full := strings.TrimSpace(p.Name.GivenName + " " + p.Name.FamilyName); full != "" {
		return full
	}
	local, _, _ := strings.Cut(p.UserName, "@")
	return local
}

func (p userPayload) isActive() bool {
	return p.Active == nil || *p.Active
}

func (h *Handlers) resource(ctx context.Context, u *identity.User) userResource {
	externalID, err := h.store.IdentitySubject(ctx, u.ID, identity.ProviderOktaSCIM)
	if err != nil {
		h.logger.Warn("failed to read externalId", "error", err)
	}
	return userResource{
		Schemas:     []string{schemaUser},
		ID:          u.ID,
		ExternalID:  externalID,
		UserName:    u.Email,
		DisplayName: u.Name,
		Active:      u.Active,
		Meta: &userMeta{
			ResourceType: "User",
			Created:      u.CreatedAt,
			LastModified: u.UpdatedAt,
			Location:     "/scim/v2/Users/" + u.ID,
		},
	}
}

// --- handlers ---

var filterRe = regexp.MustCompile(`(?i)^\s*(userName|externalId)\s+eq\s+"([^"]*)"\s*$`)

func (h *Handlers) handleList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startIndex := intQuery(r, "startIndex", 1)
	count := min(intQuery(r, "count", 100), maxPageSize)
	if startIndex < 1 {
		startIndex = 1
	}

	var matches []*identity.User
	total := 0

	if filter := r.URL.Query().Get("filter"); filter != "" {
		m := filterRe.FindStringSubmatch(filter)
		if m == nil {
			writeError(w, http.StatusBadRequest, "invalidFilter", "unsupported filter expression")
			return
		}
		var user *identity.User
		var err error
		switch strings.ToLower(m[1]) {
		case "username":
			user, err = h.store.FindByEmailAny(ctx, m[2])
		case "externalid":
			user, err = h.store.FindByIdentity(ctx, identity.ProviderOktaSCIM, m[2])
		}
		if err != nil && !errors.Is(err, identity.ErrUserNotFound) {
			h.internalError(w, err)
			return
		}
		if user != nil {
			matches = []*identity.User{user}
			total = 1
		}
	} else {
		var err error
		matches, total, err = h.store.ListUsersPage(ctx, startIndex-1, count)
		if err != nil {
			h.internalError(w, err)
			return
		}
	}

	resources := make([]userResource, len(matches))
	for i, u := range matches {
		resources[i] = h.resource(ctx, u)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schemas":      []string{schemaListResponse},
		"totalResults": total,
		"startIndex":   startIndex,
		"itemsPerPage": len(resources),
		"Resources":    resources,
	})
}

func (h *Handlers) handleCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var payload userPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.UserName == "" {
		writeError(w, http.StatusBadRequest, "invalidValue", "userName is required")
		return
	}

	user, err := h.store.CreateUser(ctx, payload.UserName, payload.displayNameOrFallback(), payload.isActive())
	if errors.Is(err, identity.ErrDuplicate) {
		writeError(w, http.StatusConflict, "uniqueness", "userName already exists")
		return
	}
	if err != nil {
		h.internalError(w, err)
		return
	}
	if payload.ExternalID != "" {
		if err := h.store.ReplaceIdentity(ctx, user.ID, identity.ProviderOktaSCIM, payload.ExternalID); err != nil {
			h.logger.Error("failed to store externalId", "error", err, "user_id", user.ID)
		}
	}
	h.logger.Info("scim user created", "user_id", user.ID)
	h.countOp("create")
	writeJSON(w, http.StatusCreated, h.resource(ctx, user))
}

func (h *Handlers) handleGet(w http.ResponseWriter, r *http.Request) {
	user, err := h.store.FindByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, identity.ErrUserNotFound) {
		writeError(w, http.StatusNotFound, "", "user not found")
		return
	}
	if err != nil {
		h.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.resource(r.Context(), user))
}

func (h *Handlers) handleReplace(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, err := h.store.FindByID(ctx, r.PathValue("id"))
	if errors.Is(err, identity.ErrUserNotFound) {
		writeError(w, http.StatusNotFound, "", "user not found")
		return
	}
	if err != nil {
		h.internalError(w, err)
		return
	}
	var payload userPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.UserName == "" {
		writeError(w, http.StatusBadRequest, "invalidValue", "userName is required")
		return
	}
	updated, err := h.applyState(ctx, user, desiredState{
		email:      payload.UserName,
		name:       payload.displayNameOrFallback(),
		active:     payload.isActive(),
		externalID: payload.ExternalID,
	})
	if err != nil {
		h.writeApplyError(w, err)
		return
	}
	h.countOp("replace")
	writeJSON(w, http.StatusOK, h.resource(ctx, updated))
}

type patchBody struct {
	Schemas    []string `json:"schemas"`
	Operations []struct {
		Op    string          `json:"op"`
		Path  string          `json:"path"`
		Value json.RawMessage `json:"value"`
	} `json:"Operations"`
}

func (h *Handlers) handlePatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, err := h.store.FindByID(ctx, r.PathValue("id"))
	if errors.Is(err, identity.ErrUserNotFound) {
		writeError(w, http.StatusNotFound, "", "user not found")
		return
	}
	if err != nil {
		h.internalError(w, err)
		return
	}

	var body patchBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalidValue", "malformed patch body")
		return
	}

	externalID, err := h.store.IdentitySubject(ctx, user.ID, identity.ProviderOktaSCIM)
	if err != nil {
		h.internalError(w, err)
		return
	}
	desired := desiredState{email: user.Email, name: user.Name, active: user.Active, externalID: externalID}

	for _, op := range body.Operations {
		if !strings.EqualFold(op.Op, "replace") {
			writeError(w, http.StatusBadRequest, "invalidValue", "only replace operations are supported")
			return
		}
		if err := applyPatchOp(&desired, op.Path, op.Value); err != nil {
			writeError(w, http.StatusBadRequest, "invalidPath", err.Error())
			return
		}
	}

	updated, err := h.applyState(ctx, user, desired)
	if err != nil {
		h.writeApplyError(w, err)
		return
	}
	h.countOp("patch")
	writeJSON(w, http.StatusOK, h.resource(ctx, updated))
}

func applyPatchOp(desired *desiredState, path string, value json.RawMessage) error {
	switch strings.ToLower(path) {
	case "":
		var payload struct {
			UserName    *string `json:"userName"`
			DisplayName *string `json:"displayName"`
			ExternalID  *string `json:"externalId"`
			Active      *bool   `json:"active"`
		}
		if err := json.Unmarshal(value, &payload); err != nil {
			return fmt.Errorf("malformed replace value")
		}
		if payload.UserName != nil {
			desired.email = *payload.UserName
		}
		if payload.DisplayName != nil {
			desired.name = *payload.DisplayName
		}
		if payload.ExternalID != nil {
			desired.externalID = *payload.ExternalID
		}
		if payload.Active != nil {
			desired.active = *payload.Active
		}
		return nil
	case "username":
		return unmarshalTo(value, &desired.email)
	case "displayname":
		return unmarshalTo(value, &desired.name)
	case "externalid":
		return unmarshalTo(value, &desired.externalID)
	case "active":
		// Okta may send booleans as strings in PATCH values.
		var b bool
		if err := json.Unmarshal(value, &b); err == nil {
			desired.active = b
			return nil
		}
		var s string
		if err := json.Unmarshal(value, &s); err == nil {
			parsed, err := strconv.ParseBool(s)
			if err == nil {
				desired.active = parsed
				return nil
			}
		}
		return fmt.Errorf("malformed active value")
	default:
		return fmt.Errorf("unsupported path %q", path)
	}
}

func unmarshalTo(raw json.RawMessage, dst *string) error {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("malformed string value")
	}
	*dst = s
	return nil
}

func (h *Handlers) handleDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, err := h.store.FindByID(ctx, r.PathValue("id"))
	if errors.Is(err, identity.ErrUserNotFound) {
		writeError(w, http.StatusNotFound, "", "user not found")
		return
	}
	if err != nil {
		h.internalError(w, err)
		return
	}
	if err := h.deactivate(ctx, user.ID); err != nil {
		h.internalError(w, err)
		return
	}
	h.countOp("delete")
	w.WriteHeader(http.StatusNoContent)
}

// desiredState is the target user state computed from PUT/PATCH input.
type desiredState struct {
	email      string
	name       string
	active     bool
	externalID string
}

// applyState reconciles the user with the desired state atomically enough
// for the SCIM client: profile first, then identity, then activation.
func (h *Handlers) applyState(ctx context.Context, user *identity.User, desired desiredState) (*identity.User, error) {
	if identity.NormalizeEmail(desired.email) != user.Email || desired.name != user.Name {
		updated, err := h.store.UpdateUser(ctx, user.ID, desired.email, desired.name)
		if err != nil {
			return nil, err
		}
		user = updated
	}
	if desired.externalID != "" {
		if err := h.store.ReplaceIdentity(ctx, user.ID, identity.ProviderOktaSCIM, desired.externalID); err != nil {
			return nil, err
		}
	}
	if desired.active != user.Active {
		if desired.active {
			if err := h.reactivate(ctx, user.ID); err != nil {
				return nil, err
			}
		} else if err := h.deactivate(ctx, user.ID); err != nil {
			return nil, err
		}
		user.Active = desired.active
	}
	return user, nil
}

// deactivate flips the flag and kills every session immediately: the
// revocation must not wait for the active-flag cache TTL.
func (h *Handlers) deactivate(ctx context.Context, userID string) error {
	if err := h.store.SetActive(ctx, userID, false); err != nil {
		return err
	}
	if err := h.sessions.DestroyAllForUser(ctx, userID); err != nil {
		// The user is already inactive; validation will fence the rest
		// within the revocation delay. Do not fail the provisioning call.
		h.logger.Error("failed to destroy sessions on deactivation", "error", err, "user_id", userID)
	}
	h.logger.Info("scim user deactivated", "user_id", userID)
	h.countOp("deactivate")
	return nil
}

func (h *Handlers) reactivate(ctx context.Context, userID string) error {
	if err := h.store.SetActive(ctx, userID, true); err != nil {
		return err
	}
	h.logger.Info("scim user reactivated", "user_id", userID)
	h.countOp("reactivate")
	return nil
}

func (h *Handlers) writeApplyError(w http.ResponseWriter, err error) {
	if errors.Is(err, identity.ErrDuplicate) {
		writeError(w, http.StatusConflict, "uniqueness", "value already in use")
		return
	}
	h.internalError(w, err)
}

// --- discovery ---

func (h *Handlers) handleServiceProviderConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"schemas":               []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
		"patch":                 map[string]any{"supported": true},
		"filter":                map[string]any{"supported": true, "maxResults": maxPageSize},
		"bulk":                  map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"sort":                  map[string]any{"supported": false},
		"etag":                  map[string]any{"supported": false},
		"changePassword":        map[string]any{"supported": false},
		"authenticationSchemes": []map[string]any{{"type": "oauthbearertoken", "name": "Bearer token", "description": "Static bearer token"}},
	})
}

func (h *Handlers) handleResourceTypes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, []map[string]any{{
		"schemas":  []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"},
		"id":       "User",
		"name":     "User",
		"endpoint": "/Users",
		"schema":   schemaUser,
	}})
}

func (h *Handlers) handleSchemas(w http.ResponseWriter, _ *http.Request) {
	attr := func(name, typ string) map[string]any {
		return map[string]any{"name": name, "type": typ, "multiValued": false, "required": name == "userName"}
	}
	writeJSON(w, http.StatusOK, []map[string]any{{
		"id":   schemaUser,
		"name": "User",
		"attributes": []map[string]any{
			attr("userName", "string"),
			attr("displayName", "string"),
			attr("externalId", "string"),
			attr("active", "boolean"),
		},
	}})
}

// --- helpers ---

func (h *Handlers) internalError(w http.ResponseWriter, err error) {
	h.logger.Error("scim request failed", "error", err)
	writeError(w, http.StatusInternalServerError, "", "internal error")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, scimType, detail string) {
	body := map[string]any{
		"schemas": []string{schemaError},
		"status":  strconv.Itoa(status),
		"detail":  detail,
	}
	if scimType != "" {
		body["scimType"] = scimType
	}
	writeJSON(w, status, body)
}

func intQuery(r *http.Request, name string, fallback int) int {
	if v := r.URL.Query().Get(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}
