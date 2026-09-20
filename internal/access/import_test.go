package access

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/paderinandrey/identity-service/internal/identity"
)

func TestImportFileValidate(t *testing.T) {
	ok := ImportFile{Assignments: []ImportEntry{
		{Email: "ada@example.com", Roles: []string{"gsh/observer", "gsh/operator"}},
		{ID: "0f2b7c1a-3d4e-4f5a-8b6c-7d8e9f0a1b2c", Roles: []string{"dfm/engineer"}},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid file rejected: %v", err)
	}

	for name, tt := range map[string]struct {
		file ImportFile
		want string
	}{
		"empty":         {ImportFile{}, "no assignments"},
		"both keys":     {ImportFile{Assignments: []ImportEntry{{ID: "x", Email: "a@example.com", Roles: []string{"gsh/observer"}}}}, "both set"},
		"no key":        {ImportFile{Assignments: []ImportEntry{{Roles: []string{"gsh/observer"}}}}, "id or email is required"},
		"no roles":      {ImportFile{Assignments: []ImportEntry{{Email: "a@example.com"}}}, "no roles"},
		"bad role ref":  {ImportFile{Assignments: []ImportEntry{{Email: "a@example.com", Roles: []string{"observer"}}}}, "want app/role"},
		"duplicate":     {ImportFile{Assignments: []ImportEntry{{Email: "A@example.com", Roles: []string{"gsh/observer"}}, {Email: "a@example.com ", Roles: []string{"gsh/operator"}}}}, "duplicates entry 1"},
		"duplicate ids": {ImportFile{Assignments: []ImportEntry{{ID: "u1", Roles: []string{"gsh/observer"}}, {ID: "u1", Roles: []string{"gsh/operator"}}}}, "duplicates entry 1"},
	} {
		t.Run(name, func(t *testing.T) {
			err := tt.file.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Validate() = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

type fakeImportUsers map[string]*identity.User // key: id or email

func (f fakeImportUsers) FindByID(_ context.Context, id string) (*identity.User, error) {
	if id == "broken" {
		return nil, errors.New("connection reset")
	}
	if u, ok := f[id]; ok {
		return u, nil
	}
	return nil, identity.ErrUserNotFound
}

func (f fakeImportUsers) FindActiveByEmail(_ context.Context, email string) (*identity.User, error) {
	if u, ok := f[email]; ok && u.Active {
		return u, nil
	}
	return nil, identity.ErrUserNotFound
}

type fakeGranter struct {
	calls   []string // "userID:ref,ref:dry"
	held    map[string]map[string]bool
	unknown map[string]bool
}

func (g *fakeGranter) GrantRoles(_ context.Context, _, userID string, refs []string, dryRun bool) (int, error) {
	g.calls = append(g.calls, fmt.Sprintf("%s:%s:%v", userID, strings.Join(refs, ","), dryRun))
	for _, ref := range refs {
		if g.unknown[ref] {
			return 0, fmt.Errorf("%w: %s", ErrRoleNotFound, ref)
		}
	}
	granted := 0
	for _, ref := range refs {
		if g.held[userID] == nil {
			g.held[userID] = map[string]bool{}
		}
		if !g.held[userID][ref] {
			granted++
			if !dryRun {
				g.held[userID][ref] = true
			}
		}
	}
	return granted, nil
}

func importFixtures() (fakeImportUsers, *fakeGranter) {
	ada := &identity.User{ID: "0f2b7c1a-3d4e-4f5a-8b6c-7d8e9f0a1b2c", Email: "ada@example.com", Active: true}
	bob := &identity.User{ID: "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d", Email: "bob@example.com", Active: true}
	users := fakeImportUsers{ada.ID: ada, ada.Email: ada, bob.ID: bob, bob.Email: bob}
	return users, &fakeGranter{held: map[string]map[string]bool{}, unknown: map[string]bool{"gsh/ghost": true}}
}

func TestImportRefusesTwoEntriesForOneUser(t *testing.T) {
	users, granter := importFixtures()
	file := ImportFile{Assignments: []ImportEntry{
		{ID: "0f2b7c1a-3d4e-4f5a-8b6c-7d8e9f0a1b2c", Roles: []string{"gsh/observer"}},
		{Email: "ada@example.com", Roles: []string{"gsh/ghost"}},
	}}
	_, err := Import(context.Background(), users, granter, "cli", file, false)
	if err == nil || !strings.Contains(err.Error(), "same user") {
		t.Fatalf("Import() = %v, want same-user refusal", err)
	}
	if len(granter.calls) != 0 {
		t.Errorf("grants were attempted before the refusal: %v", granter.calls)
	}
}

// A lookup failure that is not "no such user" — a malformed id, a
// database error — is not the entry's fault and must abort before any
// write, not be reported next to committed entries.
func TestImportAbortsOnUnexpectedLookupFailure(t *testing.T) {
	users, granter := importFixtures()
	file := ImportFile{Assignments: []ImportEntry{
		{Email: "bob@example.com", Roles: []string{"gsh/observer"}},
		{ID: "broken", Roles: []string{"gsh/observer"}},
	}}
	_, err := Import(context.Background(), users, granter, "cli", file, false)
	if err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("Import() = %v, want the lookup error", err)
	}
	if len(granter.calls) != 0 {
		t.Errorf("grants were attempted despite the abort: %v", granter.calls)
	}
}

func TestImportAppliesEntriesIndependently(t *testing.T) {
	users, granter := importFixtures()
	file := ImportFile{Assignments: []ImportEntry{
		{Email: "ada@example.com", Roles: []string{"gsh/observer", "gsh/ghost"}},
		{Email: "nobody@example.com", Roles: []string{"gsh/observer"}},
		{Email: "bob@example.com", Roles: []string{"gsh/observer", "gsh/operator"}},
	}}
	report, err := Import(context.Background(), users, granter, "cli", file, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.OK != 1 || report.Failed != 2 || len(report.Results) != 3 {
		t.Fatalf("report = %+v", report)
	}
	if report.Results[0].Err == nil || report.Results[1].Err == nil || report.Results[2].Err != nil {
		t.Errorf("per-entry outcomes = %+v", report.Results)
	}
	if got := report.Results[2]; got.Granted != 2 || got.Already != 0 {
		t.Errorf("bob = %+v, want granted 2", got)
	}

	// Rerun: idempotent, and now a dry run that must not persist.
	again, _ := Import(context.Background(), users, granter, "cli", ImportFile{Assignments: file.Assignments[2:]}, true)
	if r := again.Results[0]; r.Granted != 0 || r.Already != 2 || !again.DryRun {
		t.Errorf("rerun = %+v, want already 2 in dry-run", r)
	}
	if last := granter.calls[len(granter.calls)-1]; !strings.HasSuffix(last, ":true") {
		t.Errorf("dry-run flag not passed through: %s", last)
	}
}
