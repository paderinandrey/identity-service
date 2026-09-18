// Package cache is a bounded, TTL-expiring, single-flight lookup cache
// shared by the hot-path caches of users and permissions. It exists
// because those two caches were identical maps with the same two gaps —
// no capacity and no coalescing of concurrent misses.
package cache

import (
	"container/list"
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Metrics observes a cache; a nil Metrics disables instrumentation.
type Metrics interface {
	// Observe counts one lookup as a hit or a miss.
	Observe(hit bool)
	// SizeSet reports the number of entries after a change.
	SizeSet(n int)
	// Evicted counts one entry pushed out by capacity.
	Evicted()
}

// Loader fetches the value for a key on a miss. Returning an error leaves
// the key uncached; returning a zero value with a nil error caches it
// (negative entries are values too).
type Loader[V any] func(ctx context.Context, key string) (V, error)

// Cache is an LRU with a per-entry TTL and single-flight loading.
type Cache[V any] struct {
	capacity int
	ttl      time.Duration
	load     Loader[V]
	now      func() time.Time
	metrics  Metrics
	group    singleflight.Group

	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List // front = most recently used
	// Every entry gets the same TTL and a hit never extends it, so
	// insertion order is expiry order: the back of this list is always
	// the entry closest to expiring. Lets eviction drop dead weight in
	// O(1) before touching a live LRU victim (Codex review, PR #8).
	byExpiry *list.List // front = expires last
}

type entry[V any] struct {
	key      string
	value    V
	expires  time.Time
	byExpiry *list.Element
}

// New builds a cache holding at most capacity entries for at most ttl.
func New[V any](capacity int, ttl time.Duration, load Loader[V]) *Cache[V] {
	if capacity < 1 {
		capacity = 1
	}
	return &Cache[V]{
		capacity: capacity,
		ttl:      ttl,
		load:     load,
		now:      time.Now,
		entries:  make(map[string]*list.Element),
		order:    list.New(),
		byExpiry: list.New(),
	}
}

// SetMetrics attaches instrumentation and reports the current size at
// once, so an idle cache shows zero rather than no series at all.
func (c *Cache[V]) SetMetrics(m Metrics) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.metrics = m
	c.sizeChanged()
}

// SetClock replaces the time source; for tests of expiry.
func (c *Cache[V]) SetClock(now func() time.Time) { c.now = now }

// Len returns the number of entries, expired ones included until touched.
func (c *Cache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// loadTimeout bounds one shared load: it runs detached from any caller's
// cancellation, so it needs a deadline of its own.
const loadTimeout = 5 * time.Second

// Get returns the value for key, loading it once on a miss however many
// callers ask at the same time. The shared load is detached from the
// callers' contexts: a caller that disconnects neither cancels the load
// for the others nor stays blocked past its own deadline (Codex review,
// PR #8).
func (c *Cache[V]) Get(ctx context.Context, key string) (V, error) {
	var zero V
	if v, ok := c.peek(key); ok {
		c.observe(true)
		return v, nil
	}
	c.observe(false)

	results := c.group.DoChan(key, func() (any, error) {
		// Another caller may have loaded it while we waited for the group.
		if v, ok := c.peek(key); ok {
			return v, nil
		}
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loadTimeout)
		defer cancel()
		v, err := c.load(loadCtx, key)
		if err != nil {
			return nil, err
		}
		c.put(key, v)
		return v, nil
	})
	select {
	case r := <-results:
		if r.Err != nil {
			return zero, r.Err
		}
		return r.Val.(V), nil
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

// peek returns a live entry and marks it recently used; an expired entry
// is removed so it does not hold capacity.
func (c *Cache[V]) peek(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		var zero V
		return zero, false
	}
	e := el.Value.(*entry[V])
	if !c.now().Before(e.expires) {
		c.remove(e)
		c.sizeChanged()
		var zero V
		return zero, false
	}
	c.order.MoveToFront(el)
	return e.value, true
}

func (c *Cache[V]) put(key string, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if el, ok := c.entries[key]; ok {
		e := el.Value.(*entry[V])
		e.value, e.expires = v, now.Add(c.ttl)
		c.order.MoveToFront(el)
		c.byExpiry.MoveToFront(e.byExpiry)
		return
	}
	e := &entry[V]{key: key, value: v, expires: now.Add(c.ttl)}
	c.entries[key] = c.order.PushFront(e)
	e.byExpiry = c.byExpiry.PushFront(e)
	for c.order.Len() > c.capacity {
		// Expired entries go first — they hold no usable capacity, and a
		// recent hit may have moved one to the LRU front; only when none
		// is expired does a live entry lose its place.
		if oldest := c.byExpiry.Back().Value.(*entry[V]); !now.Before(oldest.expires) {
			c.remove(oldest)
			continue
		}
		c.remove(c.order.Back().Value.(*entry[V]))
		if c.metrics != nil {
			c.metrics.Evicted()
		}
	}
	c.sizeChanged()
}

// remove unlinks an entry from both lists and the index; callers hold mu.
func (c *Cache[V]) remove(e *entry[V]) {
	c.order.Remove(c.entries[e.key])
	c.byExpiry.Remove(e.byExpiry)
	delete(c.entries, e.key)
}

func (c *Cache[V]) sizeChanged() {
	if c.metrics != nil {
		c.metrics.SizeSet(c.order.Len())
	}
}

func (c *Cache[V]) observe(hit bool) {
	if c.metrics != nil {
		c.metrics.Observe(hit)
	}
}
