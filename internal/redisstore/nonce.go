// Package redisstore holds Redis-backed adapters for small pieces of state
// that every replica must see, such as the one-shot identifiers of the
// SAML sign-in flow.
package redisstore

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	requestKeyPrefix   = "saml:req:"
	assertionKeyPrefix = "saml:assertion:"
	// minTTL keeps SET from rejecting a zero or negative expiry.
	minTTL = time.Second
)

// NonceStore implements samlsso.NonceStore on Redis.
type NonceStore struct {
	client *redis.Client
}

// NewNonceStore wraps a connected client.
func NewNonceStore(client *redis.Client) *NonceStore {
	return &NonceStore{client: client}
}

// PutRequestID remembers an issued AuthnRequest id for ttl.
func (s *NonceStore) PutRequestID(ctx context.Context, id string, ttl time.Duration) error {
	return s.client.Set(ctx, requestKeyPrefix+id, "1", clampTTL(ttl)).Err()
}

// ConsumeRequestID atomically forgets the id and reports whether it was
// known. GETDEL makes concurrent consumers agree on a single winner.
func (s *NonceStore) ConsumeRequestID(ctx context.Context, id string) (bool, error) {
	err := s.client.GetDel(ctx, requestKeyPrefix+id).Err()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// MarkAssertionUsed records the assertion id for ttl and reports whether
// this was the first time it was seen.
func (s *NonceStore) MarkAssertionUsed(ctx context.Context, id string, ttl time.Duration) (bool, error) {
	return s.client.SetNX(ctx, assertionKeyPrefix+id, "1", clampTTL(ttl)).Result()
}

func clampTTL(ttl time.Duration) time.Duration {
	if ttl < minTTL {
		return minTTL
	}
	return ttl
}
