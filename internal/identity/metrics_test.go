package identity

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

func TestActiveCheckerMetrics(t *testing.T) {
	store := &fakeStore{findByID: map[string]*User{"u1": {ID: "u1", Active: true}}}
	checker := NewActiveChecker(store, time.Minute)
	counter := &countingCache{}
	checker.SetMetrics(counter)

	if _, err := checker.IsActive(t.Context(), "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := checker.IsActive(t.Context(), "u1"); err != nil {
		t.Fatal(err)
	}
	if counter.misses != 1 || counter.hits != 1 {
		t.Errorf("cache counters = %d hits / %d misses, want 1/1", counter.hits, counter.misses)
	}
}
