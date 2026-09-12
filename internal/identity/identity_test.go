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
	linked     map[string]bool // userID+"|"+provider
	attached   []string
	findByID   map[string]*User
	findByIDN  int
}

func key2(a, b string) string { return a + "|" + b }

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

func (f *fakeStore) HasIdentity(_ context.Context, userID, provider string) (bool, error) {
	return f.linked[key2(userID, provider)], nil
}

func (f *fakeStore) AttachIdentity(_ context.Context, userID, provider, subject string) error {
	f.attached = append(f.attached, key2(userID, key2(provider, subject)))
	return nil
}

func (f *fakeStore) FindByID(_ context.Context, id string) (*User, error) {
	f.findByIDN++
	if u, ok := f.findByID[id]; ok {
		return u, nil
	}
	return nil, ErrUserNotFound
}

func TestResolveByStoredIdentity(t *testing.T) {
	u := &User{ID: "u1", Email: "old@example.com", Active: true}
	store := &fakeStore{byIdentity: map[string]*User{key2(ProviderOkta, "subj-1"): u}}

	got, err := Resolve(t.Context(), store, ProviderOkta, "subj-1", "new-email@example.com")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.ID != "u1" {
		t.Errorf("resolved user = %s, want u1 (by stored identity, not email)", got.ID)
	}
	if len(store.attached) != 0 {
		t.Errorf("no new identity must be attached, got %v", store.attached)
	}
}

func TestResolveEmailFallbackAttachesIdentity(t *testing.T) {
	u := &User{ID: "u2", Email: "dev@example.com", Active: true}
	store := &fakeStore{
		byIdentity: map[string]*User{},
		byEmail:    map[string]*User{"dev@example.com": u},
		linked:     map[string]bool{},
	}

	got, err := Resolve(t.Context(), store, ProviderOkta, "subj-2", "  DEV@example.com ")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.ID != "u2" {
		t.Errorf("resolved user = %s, want u2", got.ID)
	}
	want := key2("u2", key2(ProviderOkta, "subj-2"))
	if len(store.attached) != 1 || store.attached[0] != want {
		t.Errorf("attached = %v, want [%s]", store.attached, want)
	}
}

func TestResolveUnknownUser(t *testing.T) {
	store := &fakeStore{byIdentity: map[string]*User{}, byEmail: map[string]*User{}}

	_, err := Resolve(t.Context(), store, ProviderOkta, "subj-x", "ghost@example.com")
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("Resolve() error = %v, want ErrUserNotFound", err)
	}
}

func TestResolveInactiveUserByIdentity(t *testing.T) {
	u := &User{ID: "u3", Active: false}
	store := &fakeStore{byIdentity: map[string]*User{key2(ProviderOkta, "subj-3"): u}}

	_, err := Resolve(t.Context(), store, ProviderOkta, "subj-3", "u3@example.com")
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("Resolve() error = %v, want ErrUserNotFound", err)
	}
}

func TestResolveRefusesRelinkByEmail(t *testing.T) {
	// The user already has an okta identity with another subject: a new
	// subject with the same email must NOT silently re-link the account.
	u := &User{ID: "u4", Email: "u4@example.com", Active: true}
	store := &fakeStore{
		byIdentity: map[string]*User{},
		byEmail:    map[string]*User{"u4@example.com": u},
		linked:     map[string]bool{key2("u4", ProviderOkta): true},
	}

	_, err := Resolve(t.Context(), store, ProviderOkta, "other-subj", "u4@example.com")
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("Resolve() error = %v, want ErrUserNotFound", err)
	}
	if len(store.attached) != 0 {
		t.Errorf("must not attach identity, got %v", store.attached)
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
	cache := NewUserCache(store, time.Minute)

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
	cache := NewUserCache(store, time.Minute)
	current := time.Now()
	cache.now = func() time.Time { return current }

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
	cache := NewUserCache(store, time.Minute)

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
