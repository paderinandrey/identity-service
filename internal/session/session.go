// Package session owns server-side browser sessions stored in Redis:
// creation with token rotation, per-user concurrency limits, logout and
// the internal validation endpoint consumed by the entry-point proxy.
package session

import (
	"context"
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
	storePrefix  = "scs:session:"
	userIndexKey = "user_sessions:" // + user UUID -> sorted set of tokens
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
	sm.Store = goredisstore.NewWithPrefix(client, storePrefix)
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

// Start rotates the session token, binds it to the user and enforces the
// per-user concurrency limit by evicting the oldest sessions.
func (m *Manager) Start(ctx context.Context, userID string) error {
	if err := m.scs.RenewToken(ctx); err != nil {
		return fmt.Errorf("renew session token: %w", err)
	}
	m.scs.Put(ctx, userIDKey, userID)

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

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
