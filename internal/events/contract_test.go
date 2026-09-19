package events

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/paderinandrey/identity-service/internal/identity"
)

var update = flag.Bool("update", false, "rewrite docs/events/examples from the current payload")

const (
	contractDir = "../../docs/events"
	// Synthetic fixtures: the examples must never carry a real person.
	exampleEventID = "5b9d0d2e-6c1e-4f6a-9d1e-1b2c3d4e5f60"
	exampleUserID  = "0f2b7c1a-3d4e-4f5a-8b6c-7d8e9f0a1b2c"
)

func exampleUser(active bool) *identity.User {
	return &identity.User{
		ID:      exampleUserID,
		Email:   "ada@example.com",
		Name:    "Ada Example",
		Active:  active,
		Version: 4,
	}
}

func fixedPayload(t *testing.T, eventType string) Payload {
	t.Helper()
	prev := now
	now = func() time.Time { return time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = prev })
	p := NewPayload(eventType, exampleUser(eventType != TypeDeactivated))
	p.ID = exampleEventID // the outbox insert assigns it in production
	return p
}

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	sch, err := c.Compile(filepath.Join(contractDir, "user-event.schema.json"))
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return sch
}

// The body the service publishes must satisfy the published schema for
// every event type — the schema is the contract consumers are written
// against, so drift between the two is a build failure.
func TestPayloadSatisfiesPublishedSchema(t *testing.T) {
	sch := compileSchema(t)
	for _, eventType := range []string{TypeCreated, TypeUpdated, TypeDeactivated, TypeReactivated, TypeSnapshot} {
		t.Run(eventType, func(t *testing.T) {
			body, err := json.Marshal(fixedPayload(t, eventType))
			if err != nil {
				t.Fatal(err)
			}
			instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			if err := sch.Validate(instance); err != nil {
				t.Errorf("payload does not satisfy docs/events/user-event.schema.json:\n%v\nbody: %s", err, body)
			}
		})
	}
}

// The schema must be strict enough to catch the mistakes it exists for:
// a field the producer never sent, a missing version, a stale schema.
func TestSchemaRejectsDrift(t *testing.T) {
	sch := compileSchema(t)
	base, _ := json.Marshal(fixedPayload(t, TypeUpdated))
	for name, mutate := range map[string]func(m map[string]any){
		"unknown top-level field": func(m map[string]any) { m["source"] = "identity" },
		"unknown user field":      func(m map[string]any) { m["user"].(map[string]any)["title"] = "x" },
		"missing version":         func(m map[string]any) { delete(m["user"].(map[string]any), "version") },
		"unknown schemaVersion":   func(m map[string]any) { m["schemaVersion"] = 2 },
		"unknown type":            func(m map[string]any) { m["type"] = "identity.user.deleted" },
		"non-uuid user id":        func(m map[string]any) { m["user"].(map[string]any)["id"] = "42" },
	} {
		t.Run(name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal(base, &m); err != nil {
				t.Fatal(err)
			}
			mutate(m)
			body, _ := json.Marshal(m)
			instance, _ := jsonschema.UnmarshalJSON(bytes.NewReader(body))
			if err := sch.Validate(instance); err == nil {
				t.Errorf("schema accepted %s: %s", name, body)
			}
		})
	}
}

// docs/events/examples/<type>.json is what the service produces for a
// fixed input, pretty-printed. Regenerate with `go test ./internal/events
// -run TestExamplesMatchPayload -update` and review the diff: an example
// that changed means the contract changed.
func TestExamplesMatchPayload(t *testing.T) {
	for _, eventType := range []string{TypeCreated, TypeUpdated, TypeDeactivated, TypeReactivated, TypeSnapshot} {
		t.Run(eventType, func(t *testing.T) {
			want, err := json.MarshalIndent(fixedPayload(t, eventType), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			want = append(want, '\n')
			path := filepath.Join(contractDir, "examples", strings.TrimPrefix(eventType, "identity.")+".json")
			if *update {
				if err := os.WriteFile(path, want, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update to generate)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs from the payload the service builds; run with -update and review the diff.\n--- want\n%s--- got\n%s", path, want, got)
			}
		})
	}
}
