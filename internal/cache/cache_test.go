package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type counting struct {
	mu                    sync.Mutex
	hits, misses, evicted int
	size                  int
}

func (m *counting) Observe(hit bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if hit {
		m.hits++
	} else {
		m.misses++
	}
}
func (m *counting) SizeSet(n int) { m.mu.Lock(); m.size = n; m.mu.Unlock() }
func (m *counting) Evicted()      { m.mu.Lock(); m.evicted++; m.mu.Unlock() }

func TestEvictsLeastRecentlyUsedAtCapacity(t *testing.T) {
	var loads atomic.Int32
	c := New(2, time.Minute, func(_ context.Context, key string) (string, error) {
		loads.Add(1)
		return "v-" + key, nil
	})
	m := &counting{}
	c.SetMetrics(m)
	ctx := t.Context()

	for _, k := range []string{"a", "b"} {
		if _, err := c.Get(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Get(ctx, "a"); err != nil { // a is now the most recent
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "c"); err != nil { // evicts b, not a
		t.Fatal(err)
	}
	if c.Len() != 2 || m.size != 2 || m.evicted != 1 {
		t.Errorf("len=%d size=%d evicted=%d; want 2/2/1", c.Len(), m.size, m.evicted)
	}
	before := loads.Load()
	if _, err := c.Get(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if loads.Load() != before {
		t.Error("a must still be cached after c was inserted")
	}
	if _, err := c.Get(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if loads.Load() != before+1 {
		t.Error("b must have been evicted and reloaded")
	}
}

func TestExpiresByTTL(t *testing.T) {
	var loads atomic.Int32
	c := New(10, time.Minute, func(_ context.Context, key string) (string, error) {
		return fmt.Sprintf("%s-%d", key, loads.Add(1)), nil
	})
	now := time.Now()
	c.SetClock(func() time.Time { return now })
	ctx := t.Context()

	first, _ := c.Get(ctx, "k")
	again, _ := c.Get(ctx, "k")
	if first != again || loads.Load() != 1 {
		t.Fatalf("second Get within TTL reloaded: %q %q loads=%d", first, again, loads.Load())
	}
	now = now.Add(2 * time.Minute)
	later, _ := c.Get(ctx, "k")
	if later == first || loads.Load() != 2 {
		t.Errorf("expired entry must reload: %q loads=%d", later, loads.Load())
	}
	if c.Len() != 1 {
		t.Errorf("len after refresh = %d, want 1 (expired entry replaced, not accumulated)", c.Len())
	}
}

func TestNegativeValuesAreCached(t *testing.T) {
	var loads atomic.Int32
	c := New(10, time.Minute, func(_ context.Context, _ string) (*string, error) {
		loads.Add(1)
		return nil, nil // "known to not exist"
	})
	for range 3 {
		if v, err := c.Get(t.Context(), "ghost"); err != nil || v != nil {
			t.Fatalf("Get = %v, %v", v, err)
		}
	}
	if loads.Load() != 1 {
		t.Errorf("negative value loaded %d times, want 1", loads.Load())
	}
}

func TestConcurrentMissesLoadOnce(t *testing.T) {
	var loads atomic.Int32
	release := make(chan struct{})
	c := New(10, time.Minute, func(_ context.Context, key string) (string, error) {
		loads.Add(1)
		<-release // hold every caller on the same in-flight load
		return "v-" + key, nil
	})

	const callers = 25
	var wg sync.WaitGroup
	results := make([]string, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = c.Get(context.Background(), "hot")
		}()
	}
	// Let the goroutines reach the loader, then release it.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if loads.Load() != 1 {
		t.Errorf("concurrent misses loaded %d times, want 1", loads.Load())
	}
	for i, r := range results {
		if r != "v-hot" {
			t.Errorf("caller %d got %q", i, r)
		}
	}
}

func TestLoadErrorsAreNotCached(t *testing.T) {
	var loads atomic.Int32
	failing := true
	c := New(10, time.Minute, func(_ context.Context, _ string) (string, error) {
		loads.Add(1)
		if failing {
			return "", errors.New("store down")
		}
		return "ok", nil
	})
	if _, err := c.Get(t.Context(), "k"); err == nil {
		t.Fatal("load error must surface")
	}
	if c.Len() != 0 {
		t.Error("a failed load must not leave an entry")
	}
	failing = false
	if v, err := c.Get(t.Context(), "k"); err != nil || v != "ok" {
		t.Errorf("after recovery: %q, %v", v, err)
	}
	if loads.Load() != 2 {
		t.Errorf("loads = %d, want 2", loads.Load())
	}
}

func TestWaitersKeepTheirOwnCancellation(t *testing.T) {
	// Codex review, PR #8: with a shared load, the first caller's context
	// must not decide the fate of the others, and a cancelled waiter must
	// not stay blocked behind a load it no longer wants.
	entered := make(chan struct{})
	release := make(chan struct{})
	c := New(10, time.Minute, func(ctx context.Context, key string) (string, error) {
		close(entered)
		select {
		case <-release:
			return "v-" + key, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})

	// First caller starts the load and then disconnects.
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := c.Get(firstCtx, "k")
		firstDone <- err
	}()
	<-entered

	// Second caller waits on the same load with a healthy context.
	secondDone := make(chan string, 1)
	go func() {
		v, err := c.Get(context.Background(), "k")
		if err != nil {
			secondDone <- "err:" + err.Error()
			return
		}
		secondDone <- v
	}()
	// Third caller gives up before the load finishes.
	thirdCtx, cancelThird := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelThird()
	thirdDone := make(chan error, 1)
	go func() {
		_, err := c.Get(thirdCtx, "k")
		thirdDone <- err
	}()

	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled first caller got %v, want context.Canceled", err)
	}
	select {
	case err := <-thirdDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("timed-out waiter got %v, want DeadlineExceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a waiter whose context expired stayed blocked behind the shared load")
	}

	// The load itself is still running for the healthy waiter.
	close(release)
	select {
	case v := <-secondDone:
		if v != "v-k" {
			t.Errorf("healthy waiter got %q; the first caller's cancellation leaked into the shared load", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("healthy waiter never got the value")
	}
}

func TestSetMetricsReportsSizeImmediately(t *testing.T) {
	c := New(10, time.Minute, func(_ context.Context, key string) (string, error) { return key, nil })
	m := &counting{size: -1}
	c.SetMetrics(m)
	if m.size != 0 {
		t.Errorf("size after SetMetrics = %d, want 0 reported at once", m.size)
	}
}
