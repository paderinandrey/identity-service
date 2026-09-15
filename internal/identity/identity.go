// Package identity holds the user domain: unified users, their external
// identities and the resolution rules used by SSO sign-in.
package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/paderinandrey/identity-service/internal/cache"
)

// Identity providers.
const (
	// ProviderOkta holds the immutable Okta user id. The same value arrives
	// as the persistent SAML NameID at sign-in and as SCIM externalId at
	// provisioning, so a single mapping serves both channels.
	ProviderOkta = "okta"
)

var (
	// ErrUserNotFound is returned when no matching active user exists.
	ErrUserNotFound = errors.New("user not found")
	// ErrDuplicate is returned when a uniqueness constraint is violated.
	ErrDuplicate = errors.New("duplicate value")
	// ErrIdentityTaken is returned when an external identity (provider,
	// subject) already belongs to another user.
	ErrIdentityTaken = errors.New("external identity belongs to another user")
)

// Provision is the state provisioning wants a user to be in. Fields are
// optional: nil means "not mentioned", and an apply takes the current value
// from the row it has locked — never from a snapshot read before the
// transaction, which a concurrent change could have made stale. Create
// requires Email; a nil Active defaults to true, a nil Name to "".
type Provision struct {
	Email *string
	Name  *string
	// Active nil leaves the flag alone; a PATCH that does not mention
	// active must not undo a deactivation that happened in between.
	Active *bool
	// ExternalID is the IdP's stable id. Nil or empty leaves the existing
	// mapping untouched.
	ExternalID *string
}

// ProvisionOutcome reports which activation transition an apply performed,
// so the caller revokes sessions only on a real deactivation.
type ProvisionOutcome struct {
	Deactivated bool
	Reactivated bool
}

// User is a unified application user.
type User struct {
	ID      string
	Email   string
	Name    string
	Active  bool
	Version int64
	// SessionEpoch is the user's session generation. A session records the
	// epoch at sign-in and is valid only while it equals this value;
	// deactivation bumps it, so revocation is durable and reactivation
	// never brings old sessions back.
	SessionEpoch int64
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
	UpsertByEmail(ctx context.Context, email, name string) (*User, error)
	TouchLastSignIn(ctx context.Context, userID string) error
}

// NormalizeEmail applies the canonical email normalization (trim + lowercase).
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// Resolve finds the user for an SSO assertion by the stable (provider,
// subject) mapping. There is no email fallback: the mapping is created by
// provisioning (SCIM externalId), never at sign-in, so an unknown subject is
// rejected even when the email matches. Inactive users are rejected as
// well; users are never created here.
func Resolve(ctx context.Context, store Store, provider, subject string) (*User, error) {
	user, err := store.FindByIdentity(ctx, provider, subject)
	if err != nil {
		return nil, err
	}
	if !user.Active {
		return nil, ErrUserNotFound
	}
	return user, nil
}

// UserCache caches user records for a bounded revocation delay, so the
// session hot path (validate is called on every ecosystem request) does
// not hit PostgreSQL every time. Both the record and the active flag are
// served from one entry; misses are cached too (as nil), so unknown ids
// cannot be used to hammer the database. Capacity, expiry and the
// coalescing of concurrent misses live in the shared cache package.
type UserCache struct {
	inner *cache.Cache[*User]
}

// NewUserCache builds a cache with the given revocation delay and
// capacity.
func NewUserCache(store Store, ttl time.Duration, capacity int) *UserCache {
	return &UserCache{inner: cache.New(capacity, ttl, func(ctx context.Context, id string) (*User, error) {
		user, err := store.FindByID(ctx, id)
		if errors.Is(err, ErrUserNotFound) {
			return nil, nil // negative entry: known to not exist
		}
		return user, err
	})}
}

// SetMetrics attaches cache instrumentation.
func (c *UserCache) SetMetrics(m cache.Metrics) { c.inner.SetMetrics(m) }

// FindByID returns the user, at most ttl stale; ErrUserNotFound when the
// user does not exist.
func (c *UserCache) FindByID(ctx context.Context, userID string) (*User, error) {
	user, err := c.inner.Get(ctx, userID)
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
	user, err := c.inner.Get(ctx, userID)
	if err != nil {
		return false, err
	}
	return user != nil && user.Active, nil
}
