package identity

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeStore struct {
	Store // panic on unimplemented methods

	byIdentity map[string]*User // provider+"|"+subject
	byEmail    map[string]*User
	findByID   map[string]*User
	findByIDN  int
}

func key2(a, b string) string { return a + "|" + b }

func (f *fakeStore) FindByID(_ context.Context, id string) (*User, error) {
	f.findByIDN++
	if u, ok := f.findByID[id]; ok {
		return u, nil
	}
	return nil, ErrUserNotFound
}

func (f *fakeStore) FindByIdentity(_ context.Context, provider, subject string) (*User, error) {
	if u, ok := f.byIdentity[key2(provider, subject)]; ok {
		return u, nil
	}
	return nil, ErrUserNotFound
}

func (f *fakeStore) FindActiveByEmail(_ context.Context, email string) (*User, error) {
	if u, ok := f.byEmail[email]; ok && u.Active {
		return u, nil
	}
	return nil, ErrUserNotFound
}

func TestResolveByStoredIdentity(t *testing.T) {
	u := &User{ID: "u1", Email: "old@example.com", Active: true}
	store := &fakeStore{byIdentity: map[string]*User{key2(ProviderOkta, "00u-1"): u}}

	got, err := Resolve(t.Context(), store, ProviderOkta, "00u-1")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.ID != "u1" {
		t.Errorf("resolved user = %s, want u1 (by stored identity)", got.ID)
	}
}

func TestResolveUnknownSubjectIgnoresMatchingEmail(t *testing.T) {
	// The subject is unknown but an active user with the assertion's email
	// exists. Linking by email is exactly what must not happen: the mapping
	// comes from provisioning, and a matching address proves nothing.
	u := &User{ID: "u2", Email: "dev@example.com", Active: true}
	store := &fakeStore{
		byIdentity: map[string]*User{},
		byEmail:    map[string]*User{"dev@example.com": u},
	}

	_, err := Resolve(t.Context(), store, ProviderOkta, "00u-unknown")
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("Resolve() error = %v, want ErrUserNotFound", err)
	}
	if len(store.byIdentity) != 0 {
		t.Errorf("no identity must be attached at sign-in, got %v", store.byIdentity)
	}
}

func TestResolveInactiveUserByIdentity(t *testing.T) {
	u := &User{ID: "u3", Active: false}
	store := &fakeStore{byIdentity: map[string]*User{key2(ProviderOkta, "00u-3"): u}}

	_, err := Resolve(t.Context(), store, ProviderOkta, "00u-3")
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("Resolve() error = %v, want ErrUserNotFound", err)
	}
}

// countingStore reports how many times the hot path reached storage —
// validate runs on every ecosystem request, so this count is a contract.
type countingStore struct {
	Store
	users map[string]*User
	calls int
}

func (s *countingStore) FindByID(_ context.Context, id string) (*User, error) {
	s.calls++
	if u, ok := s.users[id]; ok {
		return u, nil
	}
	return nil, ErrUserNotFound
}

func TestUserCacheServesRepeatedLookupsFromMemory(t *testing.T) {
	store := &countingStore{users: map[string]*User{
		"u1": {ID: "u1", Email: "u1@example.com", Active: true},
	}}
	cache := NewUserCache(store, time.Minute, 100)

	for range 5 {
		user, err := cache.FindByID(t.Context(), "u1")
		if err != nil || user.Email != "u1@example.com" {
			t.Fatalf("FindByID = %v, %v", user, err)
		}
		active, err := cache.IsActive(t.Context(), "u1")
		if err != nil || !active {
			t.Fatalf("IsActive = %v, %v", active, err)
		}
	}
	if store.calls != 1 {
		t.Errorf("storage hit %d times for 10 lookups, want 1 (warm cache must not touch the database)", store.calls)
	}
}

func TestUserCacheRefreshesAfterTTL(t *testing.T) {
	store := &countingStore{users: map[string]*User{
		"u1": {ID: "u1", Email: "old@example.com", Active: true},
	}}
	cache := NewUserCache(store, time.Minute, 100)
	current := time.Now()
	cache.inner.SetClock(func() time.Time { return current })

	if _, err := cache.FindByID(t.Context(), "u1"); err != nil {
		t.Fatal(err)
	}
	store.users["u1"] = &User{ID: "u1", Email: "new@example.com", Active: false}
	current = current.Add(2 * time.Minute)

	user, err := cache.FindByID(t.Context(), "u1")
	if err != nil || user.Email != "new@example.com" {
		t.Errorf("after TTL: FindByID = %v, %v; want refreshed record", user, err)
	}
	active, err := cache.IsActive(t.Context(), "u1")
	if err != nil || active {
		t.Errorf("deactivation must be visible after the revocation delay: %v, %v", active, err)
	}
	if store.calls != 2 {
		t.Errorf("storage calls = %d, want 2 (one per TTL window)", store.calls)
	}
}

func TestUserCacheCachesMisses(t *testing.T) {
	store := &countingStore{users: map[string]*User{}}
	cache := NewUserCache(store, time.Minute, 100)

	for range 3 {
		if _, err := cache.FindByID(t.Context(), "ghost"); !errors.Is(err, ErrUserNotFound) {
			t.Fatalf("FindByID for unknown user: err = %v, want ErrUserNotFound", err)
		}
		if active, _ := cache.IsActive(t.Context(), "ghost"); active {
			t.Error("unknown user must not be active")
		}
	}
	if store.calls != 1 {
		t.Errorf("unknown id hit storage %d times, want 1 (misses are cached too)", store.calls)
	}
}

func TestNormalizeEmail(t *testing.T) {
	if got := NormalizeEmail("  User@Example.COM "); got != "user@example.com" {
		t.Errorf("NormalizeEmail = %q", got)
	}
}
