package access

import (
	"context"
	"reflect"
	"testing"
	"time"
)

type fakeAccessStore struct {
	Store
	perms map[string][]string
	calls int
}

func (f *fakeAccessStore) EffectivePermissions(_ context.Context, userID string) ([]string, error) {
	f.calls++
	return f.perms[userID], nil
}

func TestPermissionsCacheHit(t *testing.T) {
	store := &fakeAccessStore{perms: map[string][]string{"u1": {"gsh:orders.read"}}}
	cache := NewPermissionsCache(store, time.Minute, 100)

	for range 3 {
		perms, err := cache.EffectivePermissions(t.Context(), "u1")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(perms, []string{"gsh:orders.read"}) {
			t.Errorf("perms = %v", perms)
		}
	}
	if store.calls != 1 {
		t.Errorf("store hit %d times, want 1 (cached)", store.calls)
	}
}

func TestPermissionsCacheExpiry(t *testing.T) {
	store := &fakeAccessStore{perms: map[string][]string{"u1": {"gsh:orders.read", "gsh:orders.write"}}}
	cache := NewPermissionsCache(store, time.Minute, 100)

	current := time.Now()
	cache.inner.SetClock(func() time.Time { return current })

	if _, err := cache.EffectivePermissions(t.Context(), "u1"); err != nil {
		t.Fatal(err)
	}
	// Role composition changes; the cache must pick it up after the TTL.
	store.perms["u1"] = []string{"gsh:orders.read"}
	current = current.Add(2 * time.Minute)

	perms, err := cache.EffectivePermissions(t.Context(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(perms, []string{"gsh:orders.read"}) {
		t.Errorf("perms after TTL = %v, want narrowed set", perms)
	}
}

func TestSplitRoleRef(t *testing.T) {
	app, role, err := SplitRoleRef("gsh/sourcing_manager")
	if err != nil || app != "gsh" || role != "sourcing_manager" {
		t.Errorf("SplitRoleRef = %q, %q, %v", app, role, err)
	}
	for _, bad := range []string{"", "gsh", "/role", "app/"} {
		if _, _, err := SplitRoleRef(bad); err == nil {
			t.Errorf("SplitRoleRef(%q): want error", bad)
		}
	}
}
