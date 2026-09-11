// Package identity holds the user domain: unified users, their external
// identities and the resolution rules used by SSO sign-in.
package identity

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// Identity providers.
const (
	// ProviderOkta holds SAML subjects (NameID).
	ProviderOkta = "okta"
	// ProviderOktaSCIM holds stable Okta user ids delivered by SCIM externalId.
	ProviderOktaSCIM = "okta-scim"
)

var (
	// ErrUserNotFound is returned when no matching active user exists.
	ErrUserNotFound = errors.New("user not found")
	// ErrDuplicate is returned when a uniqueness constraint is violated.
	ErrDuplicate = errors.New("duplicate value")
)

// User is a unified application user.
type User struct {
	ID           string
	Email        string
	Name         string
	Active       bool
	Version      int64
	LastSignInAt *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ExternalIdentity links a provider subject to a user.
type ExternalIdentity struct {
	ID        string
	UserID    string
	Provider  string
	Subject   string
	CreatedAt time.Time
}

// Store is the persistence contract consumed by identity operations.
type Store interface {
	FindByID(ctx context.Context, id string) (*User, error)
	FindByIdentity(ctx context.Context, provider, subject string) (*User, error)
	FindActiveByEmail(ctx context.Context, email string) (*User, error)
	HasIdentity(ctx context.Context, userID, provider string) (bool, error)
	AttachIdentity(ctx context.Context, userID, provider, subject string) error
	UpsertByEmail(ctx context.Context, email, name string) (*User, error)
	TouchLastSignIn(ctx context.Context, userID string) error
}

// NormalizeEmail applies the canonical email normalization (trim + lowercase).
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// Resolve finds the user for an SSO assertion: first by the stable
// (provider, subject) mapping, then by a one-time email fallback that links
// the subject to an active user without an identity for this provider.
// Unknown or inactive users yield ErrUserNotFound; users are never created.
func Resolve(ctx context.Context, store Store, provider, subject, email string) (*User, error) {
	user, err := store.FindByIdentity(ctx, provider, subject)
	if err != nil && !errors.Is(err, ErrUserNotFound) {
		return nil, err
	}
	if user != nil {
		if !user.Active {
			return nil, ErrUserNotFound
		}
		return user, nil
	}

	user, err = store.FindActiveByEmail(ctx, NormalizeEmail(email))
	if err != nil {
		return nil, err
	}
	linked, err := store.HasIdentity(ctx, user.ID, provider)
	if err != nil {
		return nil, err
	}
	if linked {
		// The user is already linked to a different subject of this
		// provider; do not silently re-link accounts by email.
		return nil, ErrUserNotFound
	}
	if err := store.AttachIdentity(ctx, user.ID, provider, subject); err != nil {
		return nil, err
	}
	return user, nil
}

// CacheMetrics observes cache lookups; nil disables instrumentation.
type CacheMetrics interface {
	Observe(hit bool)
}

// ActiveChecker caches the user's active flag for a bounded revocation delay,
// so per-request session validation does not hit PostgreSQL every time.
type ActiveChecker struct {
	store Store
	ttl   time.Duration

	mu      sync.Mutex
	cache   map[string]activeEntry
	now     func() time.Time
	metrics CacheMetrics
}

type activeEntry struct {
	active  bool
	expires time.Time
}

// NewActiveChecker builds a checker with the given revocation delay.
func NewActiveChecker(store Store, ttl time.Duration) *ActiveChecker {
	return &ActiveChecker{
		store: store,
		ttl:   ttl,
		cache: make(map[string]activeEntry),
		now:   time.Now,
	}
}

// SetMetrics attaches cache instrumentation.
func (c *ActiveChecker) SetMetrics(m CacheMetrics) { c.metrics = m }

// IsActive reports whether the user is currently active, at most ttl stale.
func (c *ActiveChecker) IsActive(ctx context.Context, userID string) (bool, error) {
	c.mu.Lock()
	entry, ok := c.cache[userID]
	c.mu.Unlock()
	hit := ok && c.now().Before(entry.expires)
	if c.metrics != nil {
		c.metrics.Observe(hit)
	}
	if hit {
		return entry.active, nil
	}

	user, err := c.store.FindByID(ctx, userID)
	if err != nil && !errors.Is(err, ErrUserNotFound) {
		return false, err
	}
	active := err == nil && user.Active

	c.mu.Lock()
	c.cache[userID] = activeEntry{active: active, expires: c.now().Add(c.ttl)}
	c.mu.Unlock()
	return active, nil
}
