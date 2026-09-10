package postgres

import (
	"errors"
	"testing"

	"github.com/xometry-europe-gmbh/identity-service/internal/identity"
)

func TestProvisioningStore(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()

	t.Run("create and find regardless of active", func(t *testing.T) {
		u, err := store.CreateUser(ctx, "SCIM@Example.com", "Scim User", true)
		if err != nil {
			t.Fatal(err)
		}
		if u.Email != "scim@example.com" {
			t.Errorf("email not normalized: %q", u.Email)
		}
		if err := store.SetActive(ctx, u.ID, false); err != nil {
			t.Fatal(err)
		}
		got, err := store.FindByEmailAny(ctx, "scim@EXAMPLE.com")
		if err != nil || got.ID != u.ID || got.Active {
			t.Errorf("FindByEmailAny = %+v, %v; want inactive user", got, err)
		}
	})

	t.Run("duplicate email maps to ErrDuplicate", func(t *testing.T) {
		if _, err := store.CreateUser(ctx, "dup@example.com", "One", true); err != nil {
			t.Fatal(err)
		}
		_, err := store.CreateUser(ctx, "DUP@example.com", "Two", true)
		if !errors.Is(err, identity.ErrDuplicate) {
			t.Errorf("duplicate create: err = %v, want ErrDuplicate", err)
		}
	})

	t.Run("update keeps uuid", func(t *testing.T) {
		u, err := store.CreateUser(ctx, "before@example.com", "Before", true)
		if err != nil {
			t.Fatal(err)
		}
		updated, err := store.UpdateUser(ctx, u.ID, "after@example.com", "After")
		if err != nil {
			t.Fatal(err)
		}
		if updated.ID != u.ID || updated.Email != "after@example.com" || updated.Name != "After" {
			t.Errorf("UpdateUser = %+v", updated)
		}
	})

	t.Run("replace identity updates subject and rejects theft", func(t *testing.T) {
		u1, err := store.CreateUser(ctx, "id1@example.com", "Id1", true)
		if err != nil {
			t.Fatal(err)
		}
		u2, err := store.CreateUser(ctx, "id2@example.com", "Id2", true)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ReplaceIdentity(ctx, u1.ID, identity.ProviderOktaSCIM, "ext-1"); err != nil {
			t.Fatal(err)
		}
		// externalId changes for the same user: replaced in place.
		if err := store.ReplaceIdentity(ctx, u1.ID, identity.ProviderOktaSCIM, "ext-2"); err != nil {
			t.Fatal(err)
		}
		got, err := store.FindByIdentity(ctx, identity.ProviderOktaSCIM, "ext-2")
		if err != nil || got.ID != u1.ID {
			t.Errorf("FindByIdentity after replace = %v, %v", got, err)
		}
		// Another user claiming the same subject is a duplicate.
		if err := store.ReplaceIdentity(ctx, u2.ID, identity.ProviderOktaSCIM, "ext-2"); !errors.Is(err, identity.ErrDuplicate) {
			t.Errorf("subject theft: err = %v, want ErrDuplicate", err)
		}
	})
}
