// Package access holds the unified access-control domain: applications,
// roles, permissions, role assignments and effective-permission lookup.
// Assignments change only in this service; domain checks stay in the
// business services.
package access

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Journal action names.
const (
	ActionGrant       = "role.grant"
	ActionRevoke      = "role.revoke"
	ActionComposition = "role.composition"
)

var (
	// ErrRoleNotFound is returned when app/role does not exist.
	ErrRoleNotFound = errors.New("role not found")
)

// Store is the persistence contract consumed by access operations.
// Implementations write the audit journal in the same transaction as the
// change itself.
type Store interface {
	// GrantRole assigns app/role to the user; idempotent.
	GrantRole(ctx context.Context, actor, userID, app, role string) error
	// RevokeRole removes the assignment; idempotent.
	RevokeRole(ctx context.Context, actor, userID, app, role string) error
	// EffectivePermissions returns sorted "app:permission" strings for
	// the union of the user's roles.
	EffectivePermissions(ctx context.Context, userID string) ([]string, error)
	// Seed reconciles applications, permissions and role composition with
	// the declarative config; never touches user assignments.
	Seed(ctx context.Context, actor string, cfg SeedConfig) error
}

// Application is the directory read model: an application with its
// permissions and roles.
type Application struct {
	Name        string
	Permissions []string
	Roles       []Role
}

// Role is a role with its permission composition.
type Role struct {
	Application string
	Name        string
	Permissions []string
}

// Assignment is one user-role assignment.
type Assignment struct {
	Application string
	Role        string
	GrantedBy   string
	GrantedAt   time.Time
}

// AuditEntry is one journal record.
type AuditEntry struct {
	ID           string
	Actor        string
	Action       string
	TargetUserID string // empty when the action has no user target
	Details      string // JSON
	CreatedAt    time.Time
}

// SeedConfig is the declarative bootstrap format (loaded from YAML).
type SeedConfig struct {
	Applications []SeedApplication `yaml:"applications"`
}

// SeedApplication declares one application with its permissions and roles.
type SeedApplication struct {
	Name        string     `yaml:"name"`
	Permissions []string   `yaml:"permissions"`
	Roles       []SeedRole `yaml:"roles"`
}

// SeedRole declares a role and its exact permission composition.
type SeedRole struct {
	Name        string   `yaml:"name"`
	Permissions []string `yaml:"permissions"`
}

// Validate checks internal consistency: role permissions must be declared
// on the owning application.
func (c SeedConfig) Validate() error {
	for _, app := range c.Applications {
		if strings.TrimSpace(app.Name) == "" {
			return errors.New("seed: application with empty name")
		}
		declared := make(map[string]bool, len(app.Permissions))
		for _, p := range app.Permissions {
			if strings.TrimSpace(p) == "" {
				return fmt.Errorf("seed: application %q has an empty permission", app.Name)
			}
			declared[p] = true
		}
		for _, role := range app.Roles {
			if strings.TrimSpace(role.Name) == "" {
				return fmt.Errorf("seed: application %q has a role with empty name", app.Name)
			}
			for _, p := range role.Permissions {
				if !declared[p] {
					return fmt.Errorf("seed: role %s/%s uses undeclared permission %q", app.Name, role.Name, p)
				}
			}
		}
	}
	return nil
}

// SplitRoleRef parses an "app/role" reference.
func SplitRoleRef(ref string) (app, role string, err error) {
	app, role, ok := strings.Cut(ref, "/")
	if !ok || app == "" || role == "" {
		return "", "", fmt.Errorf("invalid role reference %q (want app/role)", ref)
	}
	return app, role, nil
}

// PermissionsCache caches effective permissions for a bounded revocation
// delay, mirroring identity.ActiveChecker.
type PermissionsCache struct {
	store Store
	ttl   time.Duration

	mu    sync.Mutex
	cache map[string]permsEntry
	now   func() time.Time
}

type permsEntry struct {
	perms   []string
	expires time.Time
}

// NewPermissionsCache builds a cache with the given revocation delay.
func NewPermissionsCache(store Store, ttl time.Duration) *PermissionsCache {
	return &PermissionsCache{
		store: store,
		ttl:   ttl,
		cache: make(map[string]permsEntry),
		now:   time.Now,
	}
}

// EffectivePermissions returns the user's permissions, at most ttl stale.
func (c *PermissionsCache) EffectivePermissions(ctx context.Context, userID string) ([]string, error) {
	c.mu.Lock()
	entry, ok := c.cache[userID]
	c.mu.Unlock()
	if ok && c.now().Before(entry.expires) {
		return entry.perms, nil
	}

	perms, err := c.store.EffectivePermissions(ctx, userID)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.cache[userID] = permsEntry{perms: perms, expires: c.now().Add(c.ttl)}
	c.mu.Unlock()
	return perms, nil
}
