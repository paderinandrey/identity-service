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
}

type entry[V any] struct {
	key     string
	value   V
	expires time.Time
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
	}
}

// SetMetrics attaches instrumentation.
func (c *Cache[V]) SetMetrics(m Metrics) { c.metrics = m }

// SetClock replaces the time source; for tests of expiry.
func (c *Cache[V]) SetClock(now func() time.Time) { c.now = now }

// Len returns the number of entries, expired ones included until touched.
func (c *Cache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// Get returns the value for key, loading it once on a miss however many
// callers ask at the same time.
func (c *Cache[V]) Get(ctx context.Context, key string) (V, error) {
	if v, ok := c.peek(key); ok {
		c.observe(true)
		return v, nil
	}
	c.observe(false)

	v, err, _ := c.group.Do(key, func() (any, error) {
		// Another caller may have loaded it while we waited for the group.
		if v, ok := c.peek(key); ok {
			return v, nil
		}
		v, err := c.load(ctx, key)
		if err != nil {
			return nil, err
		}
		c.put(key, v)
		return v, nil
	})
	if err != nil {
		var zero V
		return zero, err
	}
	return v.(V), nil
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
		c.order.Remove(el)
		delete(c.entries, key)
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
	e := &entry[V]{key: key, value: v, expires: c.now().Add(c.ttl)}
	if el, ok := c.entries[key]; ok {
		el.Value = e
		c.order.MoveToFront(el)
		return
	}
	c.entries[key] = c.order.PushFront(e)
	for c.order.Len() > c.capacity {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*entry[V]).key)
		if c.metrics != nil {
			c.metrics.Evicted()
		}
	}
	c.sizeChanged()
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
