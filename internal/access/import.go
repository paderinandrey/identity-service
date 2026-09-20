package access

import (
	"fmt"
	"strings"
)

// ImportFile is the bootstrap file of role assignments: one entry per
// user, each naming the roles to grant. Consumed by the import-assignments
// CLI for a cutover such as loading GSH's users_roles.
type ImportFile struct {
	Assignments []ImportEntry `yaml:"assignments"`
}

// ImportEntry names a user by global id or by email (exactly one) and
// the roles to grant as app/role references.
type ImportEntry struct {
	ID    string   `yaml:"id"`
	Email string   `yaml:"email"`
	Roles []string `yaml:"roles"`
}

// Key identifies the entry in reports and duplicate checks.
func (e ImportEntry) Key() string {
	if e.ID != "" {
		return e.ID
	}
	return strings.ToLower(strings.TrimSpace(e.Email))
}

// Validate checks the whole file before anything is written: exactly one
// of id/email, at least one role, every role a well-formed app/role, and
// no user listed twice (two entries for one user would make the report
// ambiguous, so the file is rejected rather than merged).
func (f ImportFile) Validate() error {
	if len(f.Assignments) == 0 {
		return fmt.Errorf("import: no assignments")
	}
	seen := map[string]int{}
	for i, e := range f.Assignments {
		n := i + 1
		switch {
		case e.ID != "" && e.Email != "":
			return fmt.Errorf("import: entry %d: id and email are both set; use one", n)
		case e.ID == "" && strings.TrimSpace(e.Email) == "":
			return fmt.Errorf("import: entry %d: id or email is required", n)
		case len(e.Roles) == 0:
			return fmt.Errorf("import: entry %d (%s): no roles", n, e.Key())
		}
		for _, ref := range e.Roles {
			if _, _, err := SplitRoleRef(ref); err != nil {
				return fmt.Errorf("import: entry %d (%s): %w", n, e.Key(), err)
			}
		}
		if prev, dup := seen[e.Key()]; dup {
			return fmt.Errorf("import: entry %d duplicates entry %d (%s)", n, prev, e.Key())
		}
		seen[e.Key()] = n
	}
	return nil
}
