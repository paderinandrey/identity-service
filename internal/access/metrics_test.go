package access

import (
	"testing"
	"time"
)

type countingCache struct{ hits, misses int }

func (c *countingCache) Observe(hit bool) {
	if hit {
		c.hits++
	} else {
		c.misses++
	}
}

func TestPermissionsCacheMetrics(t *testing.T) {
	store := &fakeAccessStore{perms: map[string][]string{"u1": {"gsh:orders.read"}}}
	cache := NewPermissionsCache(store, time.Minute)
	counter := &countingCache{}
	cache.SetMetrics(counter)

	if _, err := cache.EffectivePermissions(t.Context(), "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.EffectivePermissions(t.Context(), "u1"); err != nil {
		t.Fatal(err)
	}
	if counter.misses != 1 || counter.hits != 1 {
		t.Errorf("cache counters = %d hits / %d misses, want 1/1", counter.hits, counter.misses)
	}
}
