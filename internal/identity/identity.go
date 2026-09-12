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

// UserCache caches user records for a bounded revocation delay, so the
// session hot path (validate is called on every ecosystem request) does
// not hit PostgreSQL every time. Both the record and the active flag are
// served from one entry; misses are cached too, so unknown ids cannot be
// used to hammer the database.
type UserCache struct {
	store Store
	ttl   time.Duration

	mu      sync.Mutex
	cache   map[string]userEntry
	now     func() time.Time
	metrics CacheMetrics
}

type userEntry struct {
	user    *User // nil when the user does not exist
	expires time.Time
}

// NewUserCache builds a cache with the given revocation delay.
func NewUserCache(store Store, ttl time.Duration) *UserCache {
	return &UserCache{
		store: store,
		ttl:   ttl,
		cache: make(map[string]userEntry),
		now:   time.Now,
	}
}

// SetMetrics attaches cache instrumentation.
func (c *UserCache) SetMetrics(m CacheMetrics) { c.metrics = m }

// lookup returns the cached user (nil means "known to not exist"),
// fetching and caching it on a miss.
func (c *UserCache) lookup(ctx context.Context, userID string) (*User, error) {
	c.mu.Lock()
	entry, ok := c.cache[userID]
	c.mu.Unlock()
	hit := ok && c.now().Before(entry.expires)
	if c.metrics != nil {
		c.metrics.Observe(hit)
	}
	if hit {
		return entry.user, nil
	}

	user, err := c.store.FindByID(ctx, userID)
	if err != nil && !errors.Is(err, ErrUserNotFound) {
		return nil, err
	}
	if errors.Is(err, ErrUserNotFound) {
		user = nil
	}

	c.mu.Lock()
	c.cache[userID] = userEntry{user: user, expires: c.now().Add(c.ttl)}
	c.mu.Unlock()
	return user, nil
}

// FindByID returns the user, at most ttl stale; ErrUserNotFound when the
// user does not exist.
func (c *UserCache) FindByID(ctx context.Context, userID string) (*User, error) {
	user, err := c.lookup(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrUserNotFound
	}
	return user, nil
}

// IsActive reports whether the user is currently active, at most ttl stale.
func (c *UserCache) IsActive(ctx context.Context, userID string) (bool, error) {
	user, err := c.lookup(ctx, userID)
	if err != nil {
		return false, err
	}
	return user != nil && user.Active, nil
}
