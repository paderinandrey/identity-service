package redisstore

import (
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "localhost:6380", DB: 11})
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Skipf("Redis from docker-compose is not available: %v (run `mise run up`)", err)
	}
	if err := client.FlushDB(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestRequestIDIsConsumedOnce(t *testing.T) {
	ctx := t.Context()
	store := NewNonceStore(testRedis(t))

	if err := store.PutRequestID(ctx, "req-1", time.Minute); err != nil {
		t.Fatal(err)
	}
	if known, err := store.ConsumeRequestID(ctx, "req-1"); err != nil || !known {
		t.Fatalf("first consume = %v, %v; want known", known, err)
	}
	if known, err := store.ConsumeRequestID(ctx, "req-1"); err != nil || known {
		t.Fatalf("second consume = %v, %v; want unknown (consumed)", known, err)
	}
	if known, err := store.ConsumeRequestID(ctx, "never-issued"); err != nil || known {
		t.Fatalf("unknown id = %v, %v; want unknown", known, err)
	}
}

func TestRequestIDExpires(t *testing.T) {
	ctx := t.Context()
	client := testRedis(t)
	store := NewNonceStore(client)

	if err := store.PutRequestID(ctx, "req-ttl", 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	ttl, err := client.TTL(ctx, requestKeyPrefix+"req-ttl").Result()
	if err != nil || ttl <= 0 || ttl > 3*time.Minute {
		t.Errorf("request key TTL = %v, %v; want (0, 3m]", ttl, err)
	}
}

func TestAssertionIsMarkedOnce(t *testing.T) {
	ctx := t.Context()
	client := testRedis(t)
	store := NewNonceStore(client)

	first, err := store.MarkAssertionUsed(ctx, "a-1", 2*time.Minute)
	if err != nil || !first {
		t.Fatalf("first mark = %v, %v; want first", first, err)
	}
	again, err := store.MarkAssertionUsed(ctx, "a-1", 2*time.Minute)
	if err != nil || again {
		t.Fatalf("second mark = %v, %v; want not first", again, err)
	}
	ttl, err := client.TTL(ctx, assertionKeyPrefix+"a-1").Result()
	if err != nil || ttl <= 0 {
		t.Errorf("assertion key must expire, TTL = %v, %v", ttl, err)
	}

	// A non-positive TTL (assertion at the edge of its validity) is
	// clamped rather than rejected by Redis.
	if _, err := store.MarkAssertionUsed(ctx, "a-edge", 0); err != nil {
		t.Errorf("zero ttl must be clamped, got %v", err)
	}
}
