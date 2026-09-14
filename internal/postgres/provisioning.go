package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/paderinandrey/identity-service/internal/events"
	"github.com/paderinandrey/identity-service/internal/identity"
)

const uniqueViolation = "23505"

func mapDuplicate(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return identity.ErrDuplicate
	}
	return err
}

// FindByEmailAny returns the user with the given email regardless of the
// active flag (provisioning must see deactivated users).
func (s *Store) FindByEmailAny(ctx context.Context, email string) (*identity.User, error) {
	return s.scanUser(s.pool.QueryRow(ctx,
		"SELECT "+userColumns+" FROM users WHERE email = $1", identity.NormalizeEmail(email)))
}

// inTx runs fn in a transaction on the store pool.
func (s *Store) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CreateUser inserts a new user and records the created event in the same
// transaction; identity.ErrDuplicate on an email clash.
func (s *Store) CreateUser(ctx context.Context, email, name string, active bool) (*identity.User, error) {
	var user *identity.User
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		user, err = s.scanUser(tx.QueryRow(ctx,
			`INSERT INTO users (email, name, active) VALUES ($1, $2, $3)
			 RETURNING `+userColumns, identity.NormalizeEmail(email), name, active))
		if err != nil {
			return err
		}
		return recordUserEvent(ctx, tx, events.TypeCreated, user)
	})
	if err != nil {
		return nil, mapDuplicate(err)
	}
	return user, nil
}

// UpdateUser replaces email and name, bumps the profile version and
// records the updated event; the UUID and assignments stay put.
func (s *Store) UpdateUser(ctx context.Context, id, email, name string) (*identity.User, error) {
	var user *identity.User
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		user, err = s.scanUser(tx.QueryRow(ctx,
			`UPDATE users SET email = $2, name = $3, version = version + 1, updated_at = now()
			 WHERE id = $1
			 RETURNING `+userColumns, id, identity.NormalizeEmail(email), name))
		if err != nil {
			return err
		}
		return recordUserEvent(ctx, tx, events.TypeUpdated, user)
	})
	if err != nil {
		return nil, mapDuplicate(err)
	}
	return user, nil
}

// SetActive flips the active flag, bumps the version and records the
// deactivated/reactivated event; it returns the resulting user. A no-op
// flip records nothing. Deactivation also bumps the session epoch in the
// same transaction: the revocation is durable before any session key is
// touched, and reactivation leaves the epoch alone so old sessions stay
// dead.
func (s *Store) SetActive(ctx context.Context, id string, active bool) (*identity.User, error) {
	var user *identity.User
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		current, err := s.scanUser(tx.QueryRow(ctx,
			"SELECT "+userColumns+" FROM users WHERE id = $1 FOR UPDATE", id))
		if err != nil {
			return err
		}
		if current.Active == active {
			user = current
			return nil
		}
		epochBump := 0
		if !active {
			epochBump = 1
		}
		updated, err := s.scanUser(tx.QueryRow(ctx,
			`UPDATE users
			 SET active = $2, version = version + 1, session_epoch = session_epoch + $3, updated_at = now()
			 WHERE id = $1 RETURNING `+userColumns, id, active, epochBump))
		if err != nil {
			return err
		}
		user = updated
		return recordUserEvent(ctx, tx, events.TypeForActivation(active), user)
	})
	return user, err
}

// ReplaceIdentity links (provider, subject) to the user, replacing the
// user's previous subject of that provider if any. identity.ErrDuplicate
// when the subject already belongs to another user.
func (s *Store) ReplaceIdentity(ctx context.Context, userID, provider, subject string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_identities (user_id, provider, subject) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, provider) DO UPDATE SET subject = EXCLUDED.subject`,
		userID, provider, subject)
	return mapDuplicate(err)
}

// ListUsersPage returns a page of all users (any active state) ordered by
// creation, plus the total count — SCIM list semantics.
func (s *Store) ListUsersPage(ctx context.Context, offset, limit int) ([]*identity.User, int, error) {
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx,
		"SELECT "+userColumns+" FROM users ORDER BY created_at, id OFFSET $1 LIMIT $2", offset, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	users := []*identity.User{}
	for rows.Next() {
		var u identity.User
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Active, &u.Version, &u.SessionEpoch, &u.LastSignInAt, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, 0, err
		}
		users = append(users, &u)
	}
	return users, total, rows.Err()
}

// IdentitySubject returns the user's subject for the provider, or "".
func (s *Store) IdentitySubject(ctx context.Context, userID, provider string) (string, error) {
	var subject string
	err := s.pool.QueryRow(ctx,
		"SELECT subject FROM user_identities WHERE user_id = $1 AND provider = $2",
		userID, provider).Scan(&subject)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return subject, err
}
