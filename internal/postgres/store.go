// Package postgres implements persistence adapters on top of pgx.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/paderinandrey/identity-service/internal/events"
	"github.com/paderinandrey/identity-service/internal/identity"
)

// Connect opens a pgx pool and verifies the connection.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres ping: %w", err)
	}
	return pool, nil
}

// Store implements identity.Store on PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps a pgx pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

const userColumns = "id, email, name, active, version, last_sign_in_at, created_at, updated_at"

func (s *Store) scanUser(row pgx.Row) (*identity.User, error) {
	var u identity.User
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Active, &u.Version, &u.LastSignInAt, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, identity.ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// FindByID returns the user with the given UUID regardless of active flag.
func (s *Store) FindByID(ctx context.Context, id string) (*identity.User, error) {
	return s.scanUser(s.pool.QueryRow(ctx,
		"SELECT "+userColumns+" FROM users WHERE id = $1", id))
}

// FindByIdentity returns the user linked to (provider, subject), or ErrUserNotFound.
func (s *Store) FindByIdentity(ctx context.Context, provider, subject string) (*identity.User, error) {
	return s.scanUser(s.pool.QueryRow(ctx,
		`SELECT u.id, u.email, u.name, u.active, u.version, u.last_sign_in_at, u.created_at, u.updated_at
		 FROM users u
		 JOIN user_identities i ON i.user_id = u.id
		 WHERE i.provider = $1 AND i.subject = $2`, provider, subject))
}

// FindActiveByEmail returns the active user with the given email.
func (s *Store) FindActiveByEmail(ctx context.Context, email string) (*identity.User, error) {
	return s.scanUser(s.pool.QueryRow(ctx,
		"SELECT "+userColumns+" FROM users WHERE email = $1 AND active", email))
}

// HasIdentity reports whether the user already has an identity for the provider.
func (s *Store) HasIdentity(ctx context.Context, userID, provider string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM user_identities WHERE user_id = $1 AND provider = $2)",
		userID, provider).Scan(&exists)
	return exists, err
}

// AttachIdentity links (provider, subject) to the user.
func (s *Store) AttachIdentity(ctx context.Context, userID, provider, subject string) error {
	_, err := s.pool.Exec(ctx,
		"INSERT INTO user_identities (user_id, provider, subject) VALUES ($1, $2, $3)",
		userID, provider, subject)
	return err
}

// UpsertByEmail creates the user or updates the name, idempotent by email.
// The active flag of an existing user is left untouched. Records a
// created/updated event in the same transaction; a no-op upsert (same
// name) records nothing.
func (s *Store) UpsertByEmail(ctx context.Context, email, name string) (*identity.User, error) {
	normalized := identity.NormalizeEmail(email)
	var user *identity.User
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		existing, err := s.scanUser(tx.QueryRow(ctx,
			"SELECT "+userColumns+" FROM users WHERE email = $1 FOR UPDATE", normalized))
		switch {
		case errors.Is(err, identity.ErrUserNotFound):
			user, err = s.scanUser(tx.QueryRow(ctx,
				`INSERT INTO users (email, name) VALUES ($1, $2)
				 RETURNING `+userColumns, normalized, name))
			if err != nil {
				return err
			}
			return recordUserEvent(ctx, tx, events.TypeCreated, user)
		case err != nil:
			return err
		case existing.Name == name:
			user = existing
			return nil
		default:
			user, err = s.scanUser(tx.QueryRow(ctx,
				`UPDATE users SET name = $2, version = version + 1, updated_at = now()
				 WHERE id = $1 RETURNING `+userColumns, existing.ID, name))
			if err != nil {
				return err
			}
			return recordUserEvent(ctx, tx, events.TypeUpdated, user)
		}
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

// TouchLastSignIn records a successful sign-in.
func (s *Store) TouchLastSignIn(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx,
		"UPDATE users SET last_sign_in_at = now(), updated_at = now() WHERE id = $1", userID)
	return err
}
