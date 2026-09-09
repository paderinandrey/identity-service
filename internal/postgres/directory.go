package postgres

import (
	"context"
	"sort"

	"github.com/xometry-europe-gmbh/identity-service/internal/access"
	"github.com/xometry-europe-gmbh/identity-service/internal/identity"
)

// SearchUsers returns users whose name or email contains search
// (case-insensitive); inactive users only when includeInactive is set.
// Results are ordered by name and capped by limit.
func (s *Store) SearchUsers(ctx context.Context, search string, includeInactive bool, limit int) ([]*identity.User, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT "+userColumns+` FROM users
		 WHERE (name ILIKE '%' || $1 || '%' OR email::text ILIKE '%' || $1 || '%')
		   AND (active OR $2)
		 ORDER BY name, email
		 LIMIT $3`, search, includeInactive, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := []*identity.User{}
	for rows.Next() {
		var u identity.User
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Active, &u.LastSignInAt, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, &u)
	}
	return users, rows.Err()
}

// ListApplications returns the full directory: applications with their
// permissions and roles with composition.
func (s *AccessStore) ListApplications(ctx context.Context) ([]access.Application, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT a.name, p.name FROM applications a
		 LEFT JOIN permissions p ON p.application_id = a.id
		 ORDER BY a.name, p.name`)
	if err != nil {
		return nil, err
	}
	apps := map[string]*access.Application{}
	order := []string{}
	for rows.Next() {
		var appName string
		var perm *string
		if err := rows.Scan(&appName, &perm); err != nil {
			rows.Close()
			return nil, err
		}
		app, ok := apps[appName]
		if !ok {
			app = &access.Application{Name: appName, Permissions: []string{}, Roles: []access.Role{}}
			apps[appName] = app
			order = append(order, appName)
		}
		if perm != nil {
			app.Permissions = append(app.Permissions, *perm)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	roleRows, err := s.pool.Query(ctx,
		`SELECT a.name, r.name, p.name FROM roles r
		 JOIN applications a ON a.id = r.application_id
		 LEFT JOIN role_permissions rp ON rp.role_id = r.id
		 LEFT JOIN permissions p ON p.id = rp.permission_id
		 ORDER BY a.name, r.name, p.name`)
	if err != nil {
		return nil, err
	}
	defer roleRows.Close()

	type roleKey struct{ app, role string }
	roleMap := map[roleKey]*access.Role{}
	roleOrder := []roleKey{}
	for roleRows.Next() {
		var appName, roleName string
		var perm *string
		if err := roleRows.Scan(&appName, &roleName, &perm); err != nil {
			return nil, err
		}
		key := roleKey{appName, roleName}
		role, ok := roleMap[key]
		if !ok {
			role = &access.Role{Application: appName, Name: roleName, Permissions: []string{}}
			roleMap[key] = role
			roleOrder = append(roleOrder, key)
		}
		if perm != nil {
			role.Permissions = append(role.Permissions, *perm)
		}
	}
	if err := roleRows.Err(); err != nil {
		return nil, err
	}

	for _, key := range roleOrder {
		apps[key.app].Roles = append(apps[key.app].Roles, *roleMap[key])
	}
	sort.Strings(order)
	result := make([]access.Application, 0, len(order))
	for _, name := range order {
		result = append(result, *apps[name])
	}
	return result, nil
}

// UserAssignments returns the user's role assignments.
func (s *AccessStore) UserAssignments(ctx context.Context, userID string) ([]access.Assignment, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT a.name, r.name, ur.granted_by, ur.created_at
		 FROM user_roles ur
		 JOIN roles r ON r.id = ur.role_id
		 JOIN applications a ON a.id = r.application_id
		 WHERE ur.user_id = $1
		 ORDER BY a.name, r.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	assignments := []access.Assignment{}
	for rows.Next() {
		var as access.Assignment
		if err := rows.Scan(&as.Application, &as.Role, &as.GrantedBy, &as.GrantedAt); err != nil {
			return nil, err
		}
		assignments = append(assignments, as)
	}
	return assignments, rows.Err()
}

// AuditEntries returns the newest journal records, capped by limit.
func (s *AccessStore) AuditEntries(ctx context.Context, limit int) ([]access.AuditEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, actor, action, COALESCE(target_user_id::text, ''), details::text, created_at
		 FROM access_audit_log
		 ORDER BY created_at DESC, id DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := []access.AuditEntry{}
	for rows.Next() {
		var e access.AuditEntry
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.TargetUserID, &e.Details, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
