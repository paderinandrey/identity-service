package access

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/paderinandrey/identity-service/internal/identity"
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

// ImportUsers resolves the users an import file names; the CLI wires the
// PostgreSQL store, tests a map.
type ImportUsers interface {
	FindByID(ctx context.Context, id string) (*identity.User, error)
	FindActiveByEmail(ctx context.Context, email string) (*identity.User, error)
}

// ImportGranter applies one entry's roles atomically; see
// postgres.AccessStore.GrantRoles.
type ImportGranter interface {
	GrantRoles(ctx context.Context, actor, userID string, refs []string, dryRun bool) (granted int, err error)
}

// ImportResult is one entry's outcome.
type ImportResult struct {
	Key     string
	Granted int
	Already int
	Err     error // nil when the entry was applied
}

// ImportReport is what the CLI renders.
type ImportReport struct {
	Results []ImportResult
	OK      int
	Failed  int
	DryRun  bool
}

// Import applies a validated file. Every entry is resolved before anything
// is written: two entries resolving to one user (by id and by email, say)
// would be two transactions with the second able to fail after the first
// committed, so such a file is refused with nothing written. An unknown
// user or role fails only its own entry; the rest still apply, and the
// report says which. Idempotent: rerunning the same file grants nothing.
func Import(ctx context.Context, users ImportUsers, granter ImportGranter, actor string, file ImportFile, dryRun bool) (ImportReport, error) {
	if err := file.Validate(); err != nil {
		return ImportReport{}, err
	}
	resolved := make([]*identity.User, len(file.Assignments))
	resolveErr := make([]error, len(file.Assignments))
	seen := map[string]int{}
	for i, entry := range file.Assignments {
		var user *identity.User
		var err error
		if entry.ID != "" {
			user, err = users.FindByID(ctx, entry.ID)
		} else {
			user, err = users.FindActiveByEmail(ctx, identity.NormalizeEmail(entry.Email))
		}
		if err != nil {
			// Only "no such user" is an entry's own failure. A malformed id
			// or a database error is not a fact about the file: abort here,
			// before anything is written, rather than commit the rest
			// around it (Codex review, PR #14).
			if errors.Is(err, identity.ErrUserNotFound) {
				resolveErr[i] = err
				continue
			}
			return ImportReport{}, fmt.Errorf("import: entry %d (%s): %w — nothing was written", i+1, entry.Key(), err)
		}
		if prev, dup := seen[user.ID]; dup {
			return ImportReport{}, fmt.Errorf("import: entries %d (%s) and %d (%s) are the same user %s; merge them and rerun — nothing was written",
				prev+1, file.Assignments[prev].Key(), i+1, entry.Key(), user.ID)
		}
		seen[user.ID] = i
		resolved[i] = user
	}

	report := ImportReport{DryRun: dryRun}
	for i, entry := range file.Assignments {
		res := ImportResult{Key: entry.Key(), Err: resolveErr[i]}
		if res.Err == nil {
			granted, err := granter.GrantRoles(ctx, actor, resolved[i].ID, entry.Roles, dryRun)
			res.Granted, res.Already, res.Err = granted, len(entry.Roles)-granted, err
		}
		if res.Err != nil {
			report.Failed++
		} else {
			report.OK++
		}
		report.Results = append(report.Results, res)
	}
	return report, nil
}
