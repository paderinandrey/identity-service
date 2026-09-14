// Package session owns server-side browser sessions stored in Redis:
// creation with token rotation, per-user concurrency limits, logout and
// the internal validation endpoint consumed by the entry-point proxy.
package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/alexedwards/scs/goredisstore"
	"github.com/alexedwards/scs/v2"
	"github.com/redis/go-redis/v9"
)

const (
	userIDKey    = "user_id"
	epochKey     = "session_epoch"
	storePrefix  = "scs:session:"
	userIndexKey = "user_sessions:" // + user UUID -> sorted set of tokens
	userEpochKey = "user_epoch:"    // + user UUID -> current session epoch (fence)
)

// Config carries session tuning options.
type Config struct {
	CookieName    string
	IdleTimeout   time.Duration
	Lifetime      time.Duration
	MaxConcurrent int
	SecureCookie  bool
}

// Manager wraps scs with Redis storage and a per-user session index.
type Manager struct {
	scs           *scs.SessionManager
	redis         *redis.Client
	maxConcurrent int
	logger        *slog.Logger
}

// NewManager builds the session manager on the given Redis client.
func NewManager(client *redis.Client, cfg Config, logger *slog.Logger) *Manager {
	sm := scs.New()
	sm.Store = &fencedStore{
		inner: goredisstore.NewWithPrefix(client, storePrefix),
		redis: client,
		codec: sm.Codec,
	}
	sm.IdleTimeout = cfg.IdleTimeout
	sm.Lifetime = cfg.Lifetime
	sm.Cookie.Name = cfg.CookieName
	sm.Cookie.HttpOnly = true
	sm.Cookie.Secure = cfg.SecureCookie
	sm.Cookie.SameSite = http.SameSiteLaxMode
	sm.Cookie.Path = "/"
	sm.ErrorFunc = func(w http.ResponseWriter, _ *http.Request, err error) {
		logger.Error("session store failure", "error", err)
		// Fail close with a distinct status: never treat a broken session
		// store as either authenticated or a clean 401.
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	return &Manager{
		scs:           sm,
		redis:         client,
		maxConcurrent: cfg.MaxConcurrent,
		logger:        logger,
	}
}

// Middleware loads and commits session state around a handler.
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return m.scs.LoadAndSave(next)
}

// Start rotates the session token, binds it to the user and its current
// session epoch, publishes the epoch fence and enforces the per-user
// concurrency limit by evicting the oldest sessions.
func (m *Manager) Start(ctx context.Context, userID string, epoch int64) error {
	if err := m.scs.RenewToken(ctx); err != nil {
		return fmt.Errorf("renew session token: %w", err)
	}
	m.scs.Put(ctx, userIDKey, userID)
	m.scs.Put(ctx, epochKey, epoch)
	// Idempotent: the fence mirrors the database epoch, and a sign-in is
	// the natural point to make sure it exists.
	if err := m.redis.Set(ctx, userEpochKey+userID, epoch, 0).Err(); err != nil {
		return fmt.Errorf("publish session epoch: %w", err)
	}

	token := m.scs.Token(ctx)
	indexKey := userIndexKey + userID
	now := float64(time.Now().UnixNano())

	pipe := m.redis.TxPipeline()
	pipe.ZAdd(ctx, indexKey, redis.Z{Score: now, Member: token})
	pipe.Expire(ctx, indexKey, m.scs.Lifetime)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("register session in user index: %w", err)
	}
	return m.evictOverLimit(ctx, indexKey)
}

func (m *Manager) evictOverLimit(ctx context.Context, indexKey string) error {
	count, err := m.redis.ZCard(ctx, indexKey).Result()
	if err != nil {
		return err
	}
	excess := count - int64(m.maxConcurrent)
	if excess <= 0 {
		return nil
	}
	oldest, err := m.redis.ZRange(ctx, indexKey, 0, excess-1).Result()
	if err != nil {
		return err
	}
	store, ok := m.scs.Store.(scs.CtxStore)
	if !ok {
		return fmt.Errorf("session store does not support context-aware deletion")
	}
	for _, token := range oldest {
		if err := store.DeleteCtx(ctx, token); err != nil {
			return fmt.Errorf("evict session: %w", err)
		}
	}
	return m.redis.ZRem(ctx, indexKey, toAnySlice(oldest)...).Err()
}

// UserID returns the user bound to the current session, or "".
func (m *Manager) UserID(ctx context.Context) string {
	return m.scs.GetString(ctx, userIDKey)
}

// SessionEpoch returns the epoch the current session recorded at sign-in.
// Sessions created before epochs existed report 0.
func (m *Manager) SessionEpoch(ctx context.Context) int64 {
	return m.scs.GetInt64(ctx, epochKey)
}

// Destroy terminates the current session and removes it from the user index.
func (m *Manager) Destroy(ctx context.Context) error {
	userID := m.UserID(ctx)
	token := m.scs.Token(ctx)
	if err := m.scs.Destroy(ctx); err != nil {
		return err
	}
	if userID != "" && token != "" {
		if err := m.redis.ZRem(ctx, userIndexKey+userID, token).Err(); err != nil {
			m.logger.Warn("failed to remove session from user index", "error", err)
		}
	}
	return nil
}

// DestroyAllForUser immediately terminates every session of the user:
// it raises the epoch fence to the given value first, so any session that
// recorded an older epoch is dead from this moment — including one a
// request in flight is about to write back — and only then walks the
// per-user index to delete the keys. Used by provisioning deactivation.
func (m *Manager) DestroyAllForUser(ctx context.Context, userID string, epoch int64) error {
	if err := m.redis.Set(ctx, userEpochKey+userID, epoch, 0).Err(); err != nil {
		return fmt.Errorf("raise session epoch fence: %w", err)
	}
	indexKey := userIndexKey + userID
	tokens, err := m.redis.ZRange(ctx, indexKey, 0, -1).Result()
	if err != nil {
		return fmt.Errorf("read user session index: %w", err)
	}
	store, ok := m.scs.Store.(scs.CtxStore)
	if !ok {
		return fmt.Errorf("session store does not support context-aware deletion")
	}
	for _, token := range tokens {
		if err := store.DeleteCtx(ctx, token); err != nil {
			return fmt.Errorf("destroy session: %w", err)
		}
	}
	return m.redis.Del(ctx, indexKey).Err()
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// fencedStore wraps the Redis session store with the per-user epoch fence.
// scs re-commits every loaded session under an idle timeout, so a session
// deleted while a request holds it would be written straight back; the
// only place to stop that is the store itself, below any middleware. A
// session whose recorded epoch differs from the user's fence is neither
// loaded nor saved.
type fencedStore struct {
	inner *goredisstore.RedisStore
	redis *redis.Client
	codec scs.Codec
}

var _ scs.CtxStore = (*fencedStore)(nil)

func (f *fencedStore) FindCtx(ctx context.Context, token string) ([]byte, bool, error) {
	b, found, err := f.inner.FindCtx(ctx, token)
	if err != nil || !found {
		return b, found, err
	}
	stale, err := f.stale(ctx, b)
	if err != nil {
		return nil, false, err
	}
	if stale {
		_ = f.inner.DeleteCtx(ctx, token)
		return nil, false, nil
	}
	return b, true, nil
}

func (f *fencedStore) CommitCtx(ctx context.Context, token string, b []byte, expiry time.Time) error {
	stale, err := f.stale(ctx, b)
	if err != nil {
		return err
	}
	if stale {
		// Dropping the write is the point: this is the request in flight
		// trying to resurrect a revoked session.
		return nil
	}
	return f.inner.CommitCtx(ctx, token, b, expiry)
}

func (f *fencedStore) DeleteCtx(ctx context.Context, token string) error {
	return f.inner.DeleteCtx(ctx, token)
}

// The context-free Store methods exist only to satisfy the interface; scs
// prefers the Ctx variants when the store implements CtxStore.
func (f *fencedStore) Find(token string) ([]byte, bool, error) {
	return f.FindCtx(context.Background(), token)
}

func (f *fencedStore) Commit(token string, b []byte, expiry time.Time) error {
	return f.CommitCtx(context.Background(), token, b, expiry)
}

func (f *fencedStore) Delete(token string) error {
	return f.DeleteCtx(context.Background(), token)
}

// stale reports whether the encoded session belongs to a user whose fence
// has moved past the epoch the session recorded. Anonymous sessions and
// users without a fence are never stale; a Redis error is returned so that
// scs fails closed rather than guessing.
func (f *fencedStore) stale(ctx context.Context, b []byte) (bool, error) {
	_, values, err := f.codec.Decode(b)
	if err != nil {
		// scs would fail on the same bytes a moment later; failing here
		// keeps a malformed session from being written back either.
		return false, fmt.Errorf("decode session: %w", err)
	}
	userID, _ := values[userIDKey].(string)
	if userID == "" {
		return false, nil
	}
	epoch, _ := values[epochKey].(int64)
	current, err := f.redis.Get(ctx, userEpochKey+userID).Int64()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read session epoch fence: %w", err)
	}
	return epoch != current, nil
}
