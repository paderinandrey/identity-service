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
		if err := store.ReplaceIdentity(ctx, u2.ID, identity.ProviderOkta, "ext-2"); !errors.Is(err, identity.ErrIdentityTaken) {
			t.Errorf("subject theft: err = %v, want ErrIdentityTaken", err)
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

func outboxCount(t *testing.T, store *Store) int {
	t.Helper()
	var n int
	if err := store.pool.QueryRow(t.Context(), "SELECT count(*) FROM user_events_outbox").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestProvisionCreateRollsBackOnTakenExternalID(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	if _, err := store.ProvisionCreate(ctx, identity.Provision{Email: "a@example.com", Name: "A", Active: true, ExternalID: "ext-1"}); err != nil {
		t.Fatal(err)
	}
	before := outboxCount(t, store)

	_, err := store.ProvisionCreate(ctx, identity.Provision{Email: "b@example.com", Name: "B", Active: true, ExternalID: "ext-1"})
	if !errors.Is(err, identity.ErrIdentityTaken) {
		t.Fatalf("create with taken externalId: err = %v, want ErrIdentityTaken", err)
	}
	if _, err := store.FindByEmailAny(ctx, "b@example.com"); !errors.Is(err, identity.ErrUserNotFound) {
		t.Errorf("user must be rolled back with the identity, err = %v", err)
	}
	if got := outboxCount(t, store); got != before {
		t.Errorf("outbox grew by %d on a rolled-back create", got-before)
	}

	// A duplicate email is still reported as such.
	_, err = store.ProvisionCreate(ctx, identity.Provision{Email: "A@example.com", Name: "Dup", Active: true, ExternalID: "ext-2"})
	if !errors.Is(err, identity.ErrDuplicate) {
		t.Errorf("duplicate email: err = %v, want ErrDuplicate", err)
	}
}

func TestProvisionApplyIsAtomic(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	if _, err := store.ProvisionCreate(ctx, identity.Provision{Email: "owner@example.com", Name: "Owner", Active: true, ExternalID: "ext-owner"}); err != nil {
		t.Fatal(err)
	}
	victim, err := store.ProvisionCreate(ctx, identity.Provision{Email: "victim@example.com", Name: "Victim", Active: true, ExternalID: "ext-victim"})
	if err != nil {
		t.Fatal(err)
	}
	before := outboxCount(t, store)

	_, _, err = store.ProvisionApply(ctx, victim.ID, identity.Provision{
		Email: "victim@example.com", Name: "Renamed", Active: false, ExternalID: "ext-owner",
	})
	if !errors.Is(err, identity.ErrIdentityTaken) {
		t.Fatalf("conflicting apply: err = %v, want ErrIdentityTaken", err)
	}
	after, err := store.FindByID(ctx, victim.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != "Victim" || after.Version != victim.Version || !after.Active || after.SessionEpoch != 0 {
		t.Errorf("partial writes survived the rollback: %+v", after)
	}
	if got := outboxCount(t, store); got != before {
		t.Errorf("outbox grew by %d on a rolled-back apply", got-before)
	}
}

func TestProvisionApplyProfileAndDeactivationInOneTransaction(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	u, err := store.ProvisionCreate(ctx, identity.Provision{Email: "move@example.com", Name: "Move", Active: true, ExternalID: "ext-m"})
	if err != nil {
		t.Fatal(err)
	}
	before := outboxCount(t, store)

	updated, outcome, err := store.ProvisionApply(ctx, u.ID, identity.Provision{
		Email: "moved@example.com", Name: "Moved", Active: false, ExternalID: "ext-m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Deactivated || outcome.Reactivated {
		t.Errorf("outcome = %+v, want Deactivated", outcome)
	}
	if updated.Email != "moved@example.com" || updated.Active || updated.Version != u.Version+2 || updated.SessionEpoch != 1 {
		t.Errorf("applied user = %+v, want new email, inactive, version +2, epoch 1", updated)
	}
	if got := outboxCount(t, store); got != before+2 {
		t.Errorf("outbox grew by %d, want 2 (updated + deactivated)", got-before)
	}

	// Applying the same state again is a no-op: nothing changes, nothing is recorded.
	same, outcome, err := store.ProvisionApply(ctx, u.ID, identity.Provision{
		Email: "moved@example.com", Name: "Moved", Active: false, ExternalID: "ext-m",
	})
	if err != nil || outcome.Deactivated || outcome.Reactivated || same.Version != updated.Version {
		t.Errorf("idempotent apply: %+v %+v %v", same, outcome, err)
	}
	if got := outboxCount(t, store); got != before+2 {
		t.Errorf("idempotent apply recorded %d events", got-before-2)
	}
}
