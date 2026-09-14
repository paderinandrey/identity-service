package postgres

import (
	"errors"
	"reflect"
	"testing"

	"github.com/paderinandrey/identity-service/internal/access"
	"github.com/paderinandrey/identity-service/internal/identity"
)

func seedConfig() access.SeedConfig {
	return access.SeedConfig{Applications: []access.SeedApplication{
		{
			Name:        "gsh",
			Permissions: []string{"orders.read", "orders.write"},
			Roles: []access.SeedRole{
				{Name: "sourcing_manager", Permissions: []string{"orders.read", "orders.write"}},
				{Name: "viewer", Permissions: []string{"orders.read"}},
			},
		},
		{
			Name:        "dfm",
			Permissions: []string{"audits.read"},
			Roles: []access.SeedRole{
				{Name: "engineer", Permissions: []string{"audits.read"}},
			},
		},
	}}
}

type accessEnv struct {
	store  *Store
	access *AccessStore
	user   *identity.User
}

func newAccessEnv(t *testing.T) *accessEnv {
	t.Helper()
	store := newTestStore(t)
	accessStore := NewAccessStore(store.pool)
	if err := accessStore.Seed(t.Context(), "cli", seedConfig()); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	return &accessEnv{
		store:  store,
		access: accessStore,
		user:   mustCreateUser(t, store, "granted@example.com", "Granted"),
	}
}

func (e *accessEnv) journalCount(t *testing.T, action string) int {
	t.Helper()
	var n int
	if err := e.store.pool.QueryRow(t.Context(),
		"SELECT count(*) FROM access_audit_log WHERE action = $1", action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAccessControl(t *testing.T) {
	e := newAccessEnv(t)
	ctx := t.Context()

	t.Run("seed is idempotent", func(t *testing.T) {
		before := e.journalCount(t, access.ActionComposition)
		if err := e.access.Seed(ctx, "cli", seedConfig()); err != nil {
			t.Fatalf("second Seed: %v", err)
		}
		if after := e.journalCount(t, access.ActionComposition); after != before {
			t.Errorf("second seed wrote %d extra composition entries", after-before)
		}
	})

	t.Run("grant, effective permissions, idempotency, journal", func(t *testing.T) {
		if err := e.access.GrantRole(ctx, "cli", e.user.ID, "gsh", "sourcing_manager"); err != nil {
			t.Fatal(err)
		}
		if err := e.access.GrantRole(ctx, "cli", e.user.ID, "dfm", "engineer"); err != nil {
			t.Fatal(err)
		}
		perms, err := e.access.EffectivePermissions(ctx, e.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"dfm:audits.read", "gsh:orders.read", "gsh:orders.write"}
		if !reflect.DeepEqual(perms, want) {
			t.Errorf("EffectivePermissions = %v, want %v", perms, want)
		}

		grants := e.journalCount(t, access.ActionGrant)
		if err := e.access.GrantRole(ctx, "cli", e.user.ID, "gsh", "sourcing_manager"); err != nil {
			t.Fatal(err)
		}
		if e.journalCount(t, access.ActionGrant) != grants {
			t.Error("repeated grant must be idempotent without extra journal entries")
		}
		if grants != 2 {
			t.Errorf("grant journal entries = %d, want 2", grants)
		}
	})

	t.Run("overlapping roles deduplicate", func(t *testing.T) {
		if err := e.access.GrantRole(ctx, "cli", e.user.ID, "gsh", "viewer"); err != nil {
			t.Fatal(err)
		}
		perms, err := e.access.EffectivePermissions(ctx, e.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, p := range perms {
			if p == "gsh:orders.read" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("gsh:orders.read appears %d times, want 1", count)
		}
	})

	t.Run("revoke removes permissions and journals", func(t *testing.T) {
		if err := e.access.RevokeRole(ctx, "cli", e.user.ID, "dfm", "engineer"); err != nil {
			t.Fatal(err)
		}
		perms, err := e.access.EffectivePermissions(ctx, e.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range perms {
			if p == "dfm:audits.read" {
				t.Error("revoked role permission still effective")
			}
		}
		if e.journalCount(t, access.ActionRevoke) != 1 {
			t.Error("revoke must journal exactly once")
		}
		if err := e.access.RevokeRole(ctx, "cli", e.user.ID, "dfm", "engineer"); err != nil {
			t.Fatal(err)
		}
		if e.journalCount(t, access.ActionRevoke) != 1 {
			t.Error("repeated revoke must not add journal entries")
		}
	})

	t.Run("unknown role", func(t *testing.T) {
		err := e.access.GrantRole(ctx, "cli", e.user.ID, "gsh", "ghost")
		if !errors.Is(err, access.ErrRoleNotFound) {
			t.Errorf("grant of unknown role: err = %v, want ErrRoleNotFound", err)
		}
	})

	t.Run("failed grant leaves no journal entry", func(t *testing.T) {
		before := e.journalCount(t, access.ActionGrant)
		// Unknown user id violates the user_roles FK inside the transaction.
		err := e.access.GrantRole(ctx, "cli", "00000000-0000-0000-0000-000000000001", "gsh", "viewer")
		if err == nil {
			t.Fatal("grant to unknown user must fail")
		}
		if e.journalCount(t, access.ActionGrant) != before {
			t.Error("rolled back grant must leave no journal entry")
		}
	})

	t.Run("cross-application permission is rejected by schema", func(t *testing.T) {
		var roleID, permID, gshAppID string
		if err := e.store.pool.QueryRow(ctx,
			`SELECT r.id, r.application_id FROM roles r JOIN applications a ON a.id = r.application_id
			 WHERE a.name = 'gsh' AND r.name = 'viewer'`).Scan(&roleID, &gshAppID); err != nil {
			t.Fatal(err)
		}
		if err := e.store.pool.QueryRow(ctx,
			`SELECT p.id FROM permissions p JOIN applications a ON a.id = p.application_id
			 WHERE a.name = 'dfm' AND p.name = 'audits.read'`).Scan(&permID); err != nil {
			t.Fatal(err)
		}
		_, err := e.store.pool.Exec(ctx,
			`INSERT INTO role_permissions (role_id, permission_id, application_id)
			 VALUES ($1, $2, $3)`, roleID, permID, gshAppID)
		if err == nil {
			t.Error("permission of another application must be rejected by composite FK")
		}
	})

	t.Run("narrowing role composition keeps assignments", func(t *testing.T) {
		cfg := seedConfig()
		cfg.Applications[0].Roles[0].Permissions = []string{"orders.read"} // drop orders.write
		if err := e.access.Seed(ctx, "cli", cfg); err != nil {
			t.Fatal(err)
		}
		perms, err := e.access.EffectivePermissions(ctx, e.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range perms {
			if p == "gsh:orders.write" {
				t.Error("narrowed permission still effective")
			}
		}
		var assigned int
		if err := e.store.pool.QueryRow(ctx,
			"SELECT count(*) FROM user_roles WHERE user_id = $1", e.user.ID).Scan(&assigned); err != nil {
			t.Fatal(err)
		}
		if assigned == 0 {
			t.Error("seed must not touch user role assignments")
		}
	})

	t.Run("no roles means empty set", func(t *testing.T) {
		lonely := mustCreateUser(t, e.store, "lonely@example.com", "Lonely")
		perms, err := e.access.EffectivePermissions(ctx, lonely.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(perms) != 0 {
			t.Errorf("EffectivePermissions = %v, want empty", perms)
		}
	})
}

func TestSeedValidation(t *testing.T) {
	cfg := access.SeedConfig{Applications: []access.SeedApplication{{
		Name:        "gsh",
		Permissions: []string{"orders.read"},
		Roles:       []access.SeedRole{{Name: "viewer", Permissions: []string{"undeclared.perm"}}},
	}}}
	if err := cfg.Validate(); err == nil {
		t.Error("role permission not declared on the application must fail validation")
	}
}

func TestEffectivePermissionsEmptyForInactiveUser(t *testing.T) {
	e := newAccessEnv(t)
	ctx := t.Context()
	if err := e.access.GrantRole(ctx, "cli", e.user.ID, "gsh", "viewer"); err != nil {
		t.Fatal(err)
	}
	if perms, err := e.access.EffectivePermissions(ctx, e.user.ID); err != nil || len(perms) == 0 {
		t.Fatalf("active user perms = %v, %v; want non-empty", perms, err)
	}

	if _, err := e.store.SetActive(ctx, e.user.ID, false); err != nil {
		t.Fatal(err)
	}
	perms, err := e.access.EffectivePermissions(ctx, e.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(perms) != 0 {
		t.Errorf("inactive user perms = %v, want empty (assignments are kept, rights are not)", perms)
	}
}
