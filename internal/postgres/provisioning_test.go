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
	if _, err := store.ProvisionCreate(ctx, identity.Provision{Email: new("a@example.com"), Name: new("A"), Active: new(true), ExternalID: new("ext-1")}); err != nil {
		t.Fatal(err)
	}
	before := outboxCount(t, store)

	_, err := store.ProvisionCreate(ctx, identity.Provision{Email: new("b@example.com"), Name: new("B"), Active: new(true), ExternalID: new("ext-1")})
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
	_, err = store.ProvisionCreate(ctx, identity.Provision{Email: new("A@example.com"), Name: new("Dup"), Active: new(true), ExternalID: new("ext-2")})
	if !errors.Is(err, identity.ErrDuplicate) {
		t.Errorf("duplicate email: err = %v, want ErrDuplicate", err)
	}
}

func TestProvisionApplyIsAtomic(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	if _, err := store.ProvisionCreate(ctx, identity.Provision{Email: new("owner@example.com"), Name: new("Owner"), Active: new(true), ExternalID: new("ext-owner")}); err != nil {
		t.Fatal(err)
	}
	victim, err := store.ProvisionCreate(ctx, identity.Provision{Email: new("victim@example.com"), Name: new("Victim"), Active: new(true), ExternalID: new("ext-victim")})
	if err != nil {
		t.Fatal(err)
	}
	before := outboxCount(t, store)

	_, _, err = store.ProvisionApply(ctx, victim.ID, identity.Provision{
		Email: new("victim@example.com"), Name: new("Renamed"), Active: new(false), ExternalID: new("ext-owner"),
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
	u, err := store.ProvisionCreate(ctx, identity.Provision{Email: new("move@example.com"), Name: new("Move"), Active: new(true), ExternalID: new("ext-m")})
	if err != nil {
		t.Fatal(err)
	}
	before := outboxCount(t, store)

	updated, outcome, err := store.ProvisionApply(ctx, u.ID, identity.Provision{
		Email: new("moved@example.com"), Name: new("Moved"), Active: new(false), ExternalID: new("ext-m"),
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
		Email: new("moved@example.com"), Name: new("Moved"), Active: new(false), ExternalID: new("ext-m"),
	})
	if err != nil || outcome.Deactivated || outcome.Reactivated || same.Version != updated.Version {
		t.Errorf("idempotent apply: %+v %+v %v", same, outcome, err)
	}
	if got := outboxCount(t, store); got != before+2 {
		t.Errorf("idempotent apply recorded %d events", got-before-2)
	}
}

func TestProvisionApplyOmittedFieldsComeFromTheLockedRow(t *testing.T) {
	// Codex review on PR #3: a PATCH that does not mention active must not
	// resurrect a user deactivated between the handler's read and the
	// transaction; likewise a DELETE must not restore a stale profile.
	store := newTestStore(t)
	ctx := t.Context()
	u, err := store.ProvisionCreate(ctx, identity.Provision{Email: new("race@example.com"), Name: new("Race"), ExternalID: new("ext-r")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetActive(ctx, u.ID, false); err != nil { // the concurrent deactivation
		t.Fatal(err)
	}

	renamed, outcome, err := store.ProvisionApply(ctx, u.ID, identity.Provision{Name: new("Renamed")})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Active || outcome.Reactivated || renamed.Name != "Renamed" || renamed.Email != "race@example.com" {
		t.Errorf("name-only apply = %+v %+v; must rename and keep the user inactive", renamed, outcome)
	}
	if got, _ := store.FindByIdentity(ctx, identity.ProviderOkta, "ext-r"); got == nil || got.ID != u.ID {
		t.Error("omitted externalId must leave the mapping untouched")
	}

	// A flag-only apply (DELETE) keeps the profile as it is now, not as
	// the caller once saw it.
	deleted, outcome, err := store.ProvisionApply(ctx, u.ID, identity.Provision{Active: new(false)})
	if err != nil || outcome.Deactivated || deleted.Name != "Renamed" {
		t.Errorf("flag-only apply on an inactive user = %+v %+v %v; want no-op that keeps the profile", deleted, outcome, err)
	}
}

// Title is part of the profile: set on create, changed alone with a
// version bump and an updated event, kept by an apply that omits it.
func TestProvisionTitle(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	u, err := store.ProvisionCreate(ctx, identity.Provision{
		Email: new("titled@example.com"), Name: new("Titled"), Title: new("Engineer"), Active: new(true),
	})
	if err != nil || u.Title != "Engineer" {
		t.Fatalf("create: %+v %v", u, err)
	}
	v1 := u.Version

	u, _, err = store.ProvisionApply(ctx, u.ID, identity.Provision{Title: new("Staff Engineer")})
	if err != nil || u.Title != "Staff Engineer" || u.Version != v1+1 {
		t.Fatalf("apply title: title=%q version=%d err=%v", u.Title, u.Version, err)
	}
	var events int
	if err := store.pool.QueryRow(ctx,
		"SELECT count(*) FROM user_events_outbox WHERE user_id = $1 AND event_type = 'identity.user.updated' AND payload -> 'user' ->> 'title' = 'Staff Engineer'",
		u.ID).Scan(&events); err != nil || events != 1 {
		t.Errorf("updated event with the new title: %d, %v", events, err)
	}

	u, _, err = store.ProvisionApply(ctx, u.ID, identity.Provision{Name: new("Retitled")})
	if err != nil || u.Title != "Staff Engineer" || u.Version != v1+2 {
		t.Errorf("apply without title: title=%q version=%d err=%v", u.Title, u.Version, err)
	}
	u, _, err = store.ProvisionApply(ctx, u.ID, identity.Provision{Title: new("Staff Engineer")})
	if err != nil || u.Version != v1+2 {
		t.Errorf("same title must not bump the version: version=%d err=%v", u.Version, err)
	}
}
