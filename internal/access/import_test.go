package access

import (
	"strings"
	"testing"
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
