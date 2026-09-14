package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/paderinandrey/identity-service/internal/events"
	"github.com/paderinandrey/identity-service/internal/identity"
)

const (
	uniqueViolation = "23505"
	// identitySubjectConstraint is the (provider, subject) uniqueness of
	// user_identities; its violation means the externalId belongs to
	// someone else, which SCIM reports differently from a duplicate email.
	identitySubjectConstraint = "user_identities_provider_subject_unique"
)

func mapDuplicate(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		if pgErr.ConstraintName == identitySubjectConstraint {
			return identity.ErrIdentityTaken
		}
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
		user, err = s.updateProfileTx(ctx, tx, id, email, name)
		return err
	})
	if err != nil {
		return nil, mapDuplicate(err)
	}
	return user, nil
}

// updateProfileTx is the profile update step shared by UpdateUser and
// ProvisionApply: one UPDATE plus its event, inside the caller's transaction.
func (s *Store) updateProfileTx(ctx context.Context, tx pgx.Tx, id, email, name string) (*identity.User, error) {
	user, err := s.scanUser(tx.QueryRow(ctx,
		`UPDATE users SET email = $2, name = $3, version = version + 1, updated_at = now()
		 WHERE id = $1
		 RETURNING `+userColumns, id, identity.NormalizeEmail(email), name))
	if err != nil {
		return nil, err
	}
	return user, recordUserEvent(ctx, tx, events.TypeUpdated, user)
}

// lockUserTx reads the user FOR UPDATE so the rest of the transaction
// sees a stable row.
func (s *Store) lockUserTx(ctx context.Context, tx pgx.Tx, id string) (*identity.User, error) {
	return s.scanUser(tx.QueryRow(ctx,
		"SELECT "+userColumns+" FROM users WHERE id = $1 FOR UPDATE", id))
}

// SetActive flips the active flag, bumps the version and records the
// deactivated/reactivated event; it returns the resulting user. A no-op
// flip records nothing.
func (s *Store) SetActive(ctx context.Context, id string, active bool) (*identity.User, error) {
	var user *identity.User
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		current, err := s.lockUserTx(ctx, tx, id)
		if err != nil {
			return err
		}
		user, err = s.setActiveTx(ctx, tx, current, active)
		return err
	})
	return user, err
}

// setActiveTx flips the flag of an already locked user inside the caller's
// transaction. Deactivation also bumps the session epoch there: the
// revocation is durable before any session key is touched, and
// reactivation leaves the epoch alone so old sessions stay dead. A no-op
// flip returns the user unchanged and records nothing.
func (s *Store) setActiveTx(ctx context.Context, tx pgx.Tx, current *identity.User, active bool) (*identity.User, error) {
	if current.Active == active {
		return current, nil
	}
	epochBump := 0
	if !active {
		epochBump = 1
	}
	user, err := s.scanUser(tx.QueryRow(ctx,
		`UPDATE users
		 SET active = $2, version = version + 1, session_epoch = session_epoch + $3, updated_at = now()
		 WHERE id = $1 RETURNING `+userColumns, current.ID, active, epochBump))
	if err != nil {
		return nil, err
	}
	return user, recordUserEvent(ctx, tx, events.TypeForActivation(active), user)
}

// ReplaceIdentity links (provider, subject) to the user, replacing the
// user's previous subject of that provider if any. identity.ErrDuplicate
// when the subject already belongs to another user.
func (s *Store) ReplaceIdentity(ctx context.Context, userID, provider, subject string) error {
	return mapDuplicate(replaceIdentityTx(ctx, s.pool, userID, provider, subject))
}

// execer is what replaceIdentityTx needs from either a pool or a transaction.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func replaceIdentityTx(ctx context.Context, db execer, userID, provider, subject string) error {
	_, err := db.Exec(ctx,
		`INSERT INTO user_identities (user_id, provider, subject) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, provider) DO UPDATE SET subject = EXCLUDED.subject`,
		userID, provider, subject)
	return err
}

// ProvisionCreate creates the user, its external identity and the created
// event in one transaction: a 201 from SCIM then guarantees the link
// exists. Any uniqueness conflict rolls everything back —
// identity.ErrDuplicate for the email, identity.ErrIdentityTaken for the
// externalId.
func (s *Store) ProvisionCreate(ctx context.Context, spec identity.Provision) (*identity.User, error) {
	var user *identity.User
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		user, err = s.scanUser(tx.QueryRow(ctx,
			`INSERT INTO users (email, name, active) VALUES ($1, $2, $3)
			 RETURNING `+userColumns, identity.NormalizeEmail(spec.Email), spec.Name, spec.Active))
		if err != nil {
			return err
		}
		if spec.ExternalID != "" {
			if err := replaceIdentityTx(ctx, tx, user.ID, identity.ProviderOkta, spec.ExternalID); err != nil {
				return err
			}
		}
		return recordUserEvent(ctx, tx, events.TypeCreated, user)
	})
	if err != nil {
		return nil, mapDuplicate(err)
	}
	return user, nil
}

// ProvisionApply brings the user to the desired state in one transaction:
// profile, external identity, active flag (with the session epoch) and the
// events of every step. RFC 7644 wants a PATCH applied atomically; a
// conflict in any step leaves no partial writes and no events. Steps that
// change nothing record nothing.
func (s *Store) ProvisionApply(ctx context.Context, id string, spec identity.Provision) (*identity.User, identity.ProvisionOutcome, error) {
	var user *identity.User
	var outcome identity.ProvisionOutcome
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		current, err := s.lockUserTx(ctx, tx, id)
		if err != nil {
			return err
		}
		user = current
		if identity.NormalizeEmail(spec.Email) != current.Email || spec.Name != current.Name {
			if user, err = s.updateProfileTx(ctx, tx, id, spec.Email, spec.Name); err != nil {
				return err
			}
		}
		if spec.ExternalID != "" {
			if err := replaceIdentityTx(ctx, tx, id, identity.ProviderOkta, spec.ExternalID); err != nil {
				return err
			}
		}
		if spec.Active != user.Active {
			if user, err = s.setActiveTx(ctx, tx, user, spec.Active); err != nil {
				return err
			}
			outcome.Deactivated = !spec.Active
			outcome.Reactivated = spec.Active
		}
		return nil
	})
	if err != nil {
		return nil, identity.ProvisionOutcome{}, mapDuplicate(err)
	}
	return user, outcome, nil
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
