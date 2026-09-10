package postgres

import (
	"encoding/json"
	"testing"

	"github.com/xometry-europe-gmbh/identity-service/internal/events"
)

type outboxEntry struct {
	ID        string
	EventType string
	Payload   events.Payload
}

func readOutbox(t *testing.T, store *Store) []outboxEntry {
	t.Helper()
	rows, err := store.pool.Query(t.Context(),
		"SELECT id, event_type, payload FROM user_events_outbox ORDER BY created_at, id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	entries := []outboxEntry{}
	for rows.Next() {
		var e outboxEntry
		var raw []byte
		if err := rows.Scan(&e.ID, &e.EventType, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &e.Payload); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, e)
	}
	return entries
}

func TestOutboxEvents(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()

	t.Run("create update deactivate produce versioned events", func(t *testing.T) {
		u, err := store.CreateUser(ctx, "evt@example.com", "Event User", true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpdateUser(ctx, u.ID, "evt2@example.com", "Renamed"); err != nil {
			t.Fatal(err)
		}
		if err := store.SetActive(ctx, u.ID, false); err != nil {
			t.Fatal(err)
		}

		entries := readOutbox(t, store)
		if len(entries) != 3 {
			t.Fatalf("outbox entries = %d, want 3", len(entries))
		}
		wantTypes := []string{events.TypeCreated, events.TypeUpdated, events.TypeDeactivated}
		var prevVersion int64
		for i, e := range entries {
			if e.EventType != wantTypes[i] {
				t.Errorf("entry %d type = %s, want %s", i, e.EventType, wantTypes[i])
			}
			if e.Payload.ID != e.ID {
				t.Errorf("payload id %q must equal outbox row id %q", e.Payload.ID, e.ID)
			}
			if e.Payload.SchemaVersion != events.SchemaVersion || e.Payload.User.ID != u.ID {
				t.Errorf("payload = %+v", e.Payload)
			}
			if e.Payload.User.Version <= prevVersion {
				t.Errorf("versions must grow: %d after %d", e.Payload.User.Version, prevVersion)
			}
			prevVersion = e.Payload.User.Version
		}
		if last := entries[2].Payload.User; last.Active || last.Email != "evt2@example.com" {
			t.Errorf("deactivation payload user = %+v", last)
		}
	})

	t.Run("no-op operations record nothing", func(t *testing.T) {
		u, err := store.CreateUser(ctx, "noop@example.com", "Noop", true)
		if err != nil {
			t.Fatal(err)
		}
		before := len(readOutbox(t, store))

		if err := store.SetActive(ctx, u.ID, true); err != nil { // already active
			t.Fatal(err)
		}
		if _, err := store.UpsertByEmail(ctx, "noop@example.com", "Noop"); err != nil { // same name
			t.Fatal(err)
		}
		if err := store.TouchLastSignIn(ctx, u.ID); err != nil {
			t.Fatal(err)
		}
		if after := len(readOutbox(t, store)); after != before {
			t.Errorf("no-op operations wrote %d events", after-before)
		}
	})

	t.Run("failed change leaves no event", func(t *testing.T) {
		if _, err := store.CreateUser(ctx, "taken@example.com", "Taken", true); err != nil {
			t.Fatal(err)
		}
		before := len(readOutbox(t, store))
		if _, err := store.CreateUser(ctx, "taken@example.com", "Clone", true); err == nil {
			t.Fatal("duplicate create must fail")
		}
		if after := len(readOutbox(t, store)); after != before {
			t.Error("rolled back change must leave no event")
		}
	})

	t.Run("replay enqueues snapshots with current versions", func(t *testing.T) {
		before := len(readOutbox(t, store))
		var total int
		if err := store.pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&total); err != nil {
			t.Fatal(err)
		}
		n, err := store.EnqueueSnapshots(ctx, 2) // small batch to exercise paging
		if err != nil {
			t.Fatal(err)
		}
		if n != total {
			t.Errorf("EnqueueSnapshots = %d, want %d", n, total)
		}
		entries := readOutbox(t, store)[before:]
		if len(entries) != total {
			t.Fatalf("snapshot events = %d, want %d", len(entries), total)
		}
		for _, e := range entries {
			if e.EventType != events.TypeSnapshot || e.Payload.User.Version < 1 {
				t.Errorf("snapshot entry = %s %+v", e.EventType, e.Payload.User)
			}
		}
	})
}
