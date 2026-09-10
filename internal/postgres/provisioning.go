package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/xometry-europe-gmbh/identity-service/internal/identity"
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

// CreateUser inserts a new user; identity.ErrDuplicate on an email clash.
func (s *Store) CreateUser(ctx context.Context, email, name string, active bool) (*identity.User, error) {
	user, err := s.scanUser(s.pool.QueryRow(ctx,
		`INSERT INTO users (email, name, active) VALUES ($1, $2, $3)
		 RETURNING `+userColumns, identity.NormalizeEmail(email), name, active))
	if err != nil {
		return nil, mapDuplicate(err)
	}
	return user, nil
}

// UpdateUser replaces email and name; the UUID and assignments stay put.
func (s *Store) UpdateUser(ctx context.Context, id, email, name string) (*identity.User, error) {
	user, err := s.scanUser(s.pool.QueryRow(ctx,
		`UPDATE users SET email = $2, name = $3, updated_at = now() WHERE id = $1
		 RETURNING `+userColumns, id, identity.NormalizeEmail(email), name))
	if err != nil {
		return nil, mapDuplicate(err)
	}
	return user, nil
}

// SetActive flips the active flag.
func (s *Store) SetActive(ctx context.Context, id string, active bool) error {
	tag, err := s.pool.Exec(ctx,
		"UPDATE users SET active = $2, updated_at = now() WHERE id = $1", id, active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return identity.ErrUserNotFound
	}
	return nil
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
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Active, &u.LastSignInAt, &u.CreatedAt, &u.UpdatedAt); err != nil {
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
