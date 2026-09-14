package postgres

import (
	"errors"
	"testing"

	"github.com/paderinandrey/identity-service/internal/identity"
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
		if _, err := store.SetActive(ctx, u.ID, false); err != nil {
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
		if err := store.ReplaceIdentity(ctx, u1.ID, identity.ProviderOkta, "ext-1"); err != nil {
			t.Fatal(err)
		}
		// externalId changes for the same user: replaced in place.
		if err := store.ReplaceIdentity(ctx, u1.ID, identity.ProviderOkta, "ext-2"); err != nil {
			t.Fatal(err)
		}
		got, err := store.FindByIdentity(ctx, identity.ProviderOkta, "ext-2")
		if err != nil || got.ID != u1.ID {
			t.Errorf("FindByIdentity after replace = %v, %v", got, err)
		}
		// Another user claiming the same subject is a duplicate.
		if err := store.ReplaceIdentity(ctx, u2.ID, identity.ProviderOkta, "ext-2"); !errors.Is(err, identity.ErrDuplicate) {
			t.Errorf("subject theft: err = %v, want ErrDuplicate", err)
		}
	})
}

func TestSetActiveBumpsSessionEpochOnlyOnDeactivation(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	u := mustCreateUser(t, store, "epoch@example.com", "Epoch")
	if u.SessionEpoch != 0 {
		t.Fatalf("new user epoch = %d, want 0", u.SessionEpoch)
	}

	deactivated, err := store.SetActive(ctx, u.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if deactivated.Active || deactivated.SessionEpoch != 1 {
		t.Errorf("after deactivation: active=%v epoch=%d, want inactive / epoch 1", deactivated.Active, deactivated.SessionEpoch)
	}

	// Repeating the same transition is a no-op: no second bump.
	again, err := store.SetActive(ctx, u.ID, false)
	if err != nil || again.SessionEpoch != 1 {
		t.Errorf("idempotent deactivation: epoch=%d err=%v, want 1", again.SessionEpoch, err)
	}

	// Reactivation must not move the epoch, or old sessions could return.
	reactivated, err := store.SetActive(ctx, u.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reactivated.Active || reactivated.SessionEpoch != 1 {
		t.Errorf("after reactivation: active=%v epoch=%d, want active / epoch 1", reactivated.Active, reactivated.SessionEpoch)
	}

	got, err := store.FindByID(ctx, u.ID)
	if err != nil || got.SessionEpoch != 1 {
		t.Errorf("persisted epoch = %d, %v; want 1", got.SessionEpoch, err)
	}
}
