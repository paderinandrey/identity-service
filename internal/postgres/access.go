package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/xometry-europe-gmbh/identity-service/internal/access"
)

// AccessStore implements access.Store on PostgreSQL. Every mutation writes
// its audit-journal entry in the same transaction.
type AccessStore struct {
	pool Pool
}

// Pool is the subset of pgxpool.Pool used by AccessStore.
type Pool interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// NewAccessStore wraps a pgx pool.
func NewAccessStore(pool Pool) *AccessStore {
	return &AccessStore{pool: pool}
}

func (s *AccessStore) withTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
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

func findRoleID(ctx context.Context, tx pgx.Tx, app, role string) (string, error) {
	var id string
	err := tx.QueryRow(ctx,
		`SELECT r.id FROM roles r JOIN applications a ON a.id = r.application_id
		 WHERE a.name = $1 AND r.name = $2`, app, role).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("%w: %s/%s", access.ErrRoleNotFound, app, role)
	}
	return id, err
}

func writeJournal(ctx context.Context, tx pgx.Tx, actor, action, targetUserID, roleID string, details map[string]any) error {
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	var target, role *string
	if targetUserID != "" {
		target = &targetUserID
	}
	if roleID != "" {
		role = &roleID
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO access_audit_log (actor, action, target_user_id, role_id, details)
		 VALUES ($1, $2, $3, $4, $5)`, actor, action, target, role, raw)
	return err
}

// GrantRole assigns app/role to the user; idempotent, journaled.
func (s *AccessStore) GrantRole(ctx context.Context, actor, userID, app, role string) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		roleID, err := findRoleID(ctx, tx, app, role)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`INSERT INTO user_roles (user_id, role_id, granted_by) VALUES ($1, $2, $3)
			 ON CONFLICT (user_id, role_id) DO NOTHING`, userID, roleID, actor)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // already granted: idempotent, no journal noise
		}
		return writeJournal(ctx, tx, actor, access.ActionGrant, userID, roleID,
			map[string]any{"application": app, "role": role})
	})
}

// RevokeRole removes the assignment; idempotent, journaled.
func (s *AccessStore) RevokeRole(ctx context.Context, actor, userID, app, role string) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		roleID, err := findRoleID(ctx, tx, app, role)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			"DELETE FROM user_roles WHERE user_id = $1 AND role_id = $2", userID, roleID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		return writeJournal(ctx, tx, actor, access.ActionRevoke, userID, roleID,
			map[string]any{"application": app, "role": role})
	})
}

// EffectivePermissions returns sorted "app:permission" for the user's roles.
func (s *AccessStore) EffectivePermissions(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT a.name || ':' || p.name
		 FROM user_roles ur
		 JOIN role_permissions rp ON rp.role_id = ur.role_id
		 JOIN permissions p ON p.id = rp.permission_id
		 JOIN applications a ON a.id = p.application_id
		 WHERE ur.user_id = $1
		 ORDER BY 1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	perms := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		perms = append(perms, p)
	}
	return perms, rows.Err()
}

// Seed reconciles applications, permissions and role composition with the
// config: creates what is missing, aligns role composition exactly, never
// deletes applications/roles/permissions and never touches user_roles.
func (s *AccessStore) Seed(ctx context.Context, actor string, cfg access.SeedConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	return s.withTx(ctx, func(tx pgx.Tx) error {
		for _, app := range cfg.Applications {
			var appID string
			if err := tx.QueryRow(ctx,
				`INSERT INTO applications (name) VALUES ($1)
				 ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
				 RETURNING id`, app.Name).Scan(&appID); err != nil {
				return err
			}

			permIDs := make(map[string]string, len(app.Permissions))
			for _, perm := range app.Permissions {
				var id string
				if err := tx.QueryRow(ctx,
					`INSERT INTO permissions (application_id, name) VALUES ($1, $2)
					 ON CONFLICT (application_id, name) DO UPDATE SET name = EXCLUDED.name
					 RETURNING id`, appID, perm).Scan(&id); err != nil {
					return err
				}
				permIDs[perm] = id
			}

			for _, role := range app.Roles {
				var roleID string
				if err := tx.QueryRow(ctx,
					`INSERT INTO roles (application_id, name) VALUES ($1, $2)
					 ON CONFLICT (application_id, name) DO UPDATE SET name = EXCLUDED.name
					 RETURNING id`, appID, role.Name).Scan(&roleID); err != nil {
					return err
				}
				changed, err := alignRoleComposition(ctx, tx, roleID, appID, role.Permissions, permIDs)
				if err != nil {
					return err
				}
				if changed {
					if err := writeJournal(ctx, tx, actor, access.ActionComposition, "", roleID,
						map[string]any{"application": app.Name, "role": role.Name, "permissions": role.Permissions}); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

// alignRoleComposition brings role_permissions of a role exactly to want.
func alignRoleComposition(ctx context.Context, tx pgx.Tx, roleID, appID string, want []string, permIDs map[string]string) (bool, error) {
	wantIDs := make(map[string]bool, len(want))
	for _, p := range want {
		wantIDs[permIDs[p]] = true
	}

	rows, err := tx.Query(ctx,
		"SELECT permission_id FROM role_permissions WHERE role_id = $1", roleID)
	if err != nil {
		return false, err
	}
	haveIDs := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return false, err
		}
		haveIDs[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	changed := false
	for id := range wantIDs {
		if !haveIDs[id] {
			if _, err := tx.Exec(ctx,
				`INSERT INTO role_permissions (role_id, permission_id, application_id)
				 VALUES ($1, $2, $3)`, roleID, id, appID); err != nil {
				return false, err
			}
			changed = true
		}
	}
	for id := range haveIDs {
		if !wantIDs[id] {
			if _, err := tx.Exec(ctx,
				"DELETE FROM role_permissions WHERE role_id = $1 AND permission_id = $2", roleID, id); err != nil {
				return false, err
			}
			changed = true
		}
	}
	return changed, nil
}
