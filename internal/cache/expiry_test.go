package cache

import (
	"context"
	"testing"
	"time"
)

type evictionCounter struct{ evicted, size int }

func (m *evictionCounter) Observe(bool)  {}
func (m *evictionCounter) SizeSet(n int) { m.size = n }
func (m *evictionCounter) Evicted()      { m.evicted++ }

// A hit moves an entry to the LRU front without extending its TTL, so an
// expired entry can sit in front of live ones. Eviction must drop the
// expired entry, not a live one from the tail (Codex review, PR #8).
func TestEvictionPurgesExpiredBeforeLiveEntries(t *testing.T) {
	clock := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	loads := map[string]int{}
	c := New(2, time.Minute, func(_ context.Context, key string) (string, error) {
		loads[key]++
		return "v-" + key, nil
	})
	c.SetClock(func() time.Time { return clock })
	m := &evictionCounter{}
	c.SetMetrics(m)
	ctx := context.Background()

	must := func(key string) {
		t.Helper()
		if _, err := c.Get(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	must("a") // expires at 12:01:00
	clock = clock.Add(30 * time.Second)
	must("b") // expires at 12:01:30
	clock = clock.Add(29 * time.Second)
	must("a")                          // hit: a moves to the LRU front, TTL untouched
	clock = clock.Add(2 * time.Second) // 12:01:01 — a expired, b still live
	must("c")                          // cache is full: something has to go

	if loads["b"] != 1 {
		t.Fatalf("b loaded %d times before the check", loads["b"])
	}
	must("b")
	if loads["b"] != 1 {
		t.Errorf("live entry b was evicted in favour of the expired a: loads = %v", loads)
	}
	if m.evicted != 0 {
		t.Errorf("evictions = %d, want 0: dropping an expired entry is not a capacity eviction", m.evicted)
	}
	if got := c.Len(); got != 2 {
		t.Errorf("Len() = %d, want 2", got)
	}

	// With nothing expired the LRU tail still goes, and it counts.
	must("d")
	if m.evicted != 1 {
		t.Errorf("evictions = %d, want 1 after a live eviction", m.evicted)
	}
}
