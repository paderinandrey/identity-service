package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

func body(t *testing.T, id string, eventType string, user UserSnapshot) []byte {
	t.Helper()
	b, err := json.Marshal(Event{ID: id, Type: eventType, SchemaVersion: 1, OccurredAt: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC), User: user})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var ada = UserSnapshot{ID: "0f2b7c1a-3d4e-4f5a-8b6c-7d8e9f0a1b2c", Email: "ada@example.com", Name: "Ada Example", Active: true, Version: 4}

func TestDuplicateEventIsIgnored(t *testing.T) {
	p := NewProjection()
	msg := body(t, "e1", "identity.user.updated", ada)
	if got := p.Apply(msg); got != Applied {
		t.Fatalf("first delivery = %s, want applied", got)
	}
	if got := p.Apply(msg); got != Duplicate {
		t.Errorf("redelivery = %s, want duplicate", got)
	}
	if s := p.Stats(); s.Applied != 1 || s.Duplicates != 1 {
		t.Errorf("stats = %+v", s)
	}
}

func TestStaleVersionDoesNotOverwrite(t *testing.T) {
	p := NewProjection()
	p.Apply(body(t, "e4", "identity.user.updated", ada))
	older := ada
	older.Version, older.Name = 3, "Old Name"
	if got := p.Apply(body(t, "e3", "identity.user.updated", older)); got != Stale {
		t.Errorf("older version = %s, want stale", got)
	}
	same := ada
	same.Name = "Same Version Different Name"
	if got := p.Apply(body(t, "e4b", "identity.user.snapshot", same)); got != Stale {
		t.Errorf("equal version = %s, want stale", got)
	}
	if u, _ := p.User(ada.ID); u.Name != ada.Name || u.Version != 4 {
		t.Errorf("projection changed by a stale event: %+v", u)
	}
}

// Events N and N+1 of one user arrive at the same time in either order:
// the projection must end at N+1 regardless.
func TestConcurrentEventsEndWithHigherVersion(t *testing.T) {
	for _, order := range [][]int64{{5, 6}, {6, 5}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			p := NewProjection()
			var wg sync.WaitGroup
			for _, v := range order {
				u := ada
				u.Version, u.Name = v, fmt.Sprintf("v%d", v)
				msg := body(t, fmt.Sprintf("e%d", v), "identity.user.updated", u)
				wg.Add(1)
				go func() { defer wg.Done(); p.Apply(msg) }()
			}
			wg.Wait()
			if u, _ := p.User(ada.ID); u.Version != 6 || u.Name != "v6" {
				t.Errorf("projection = %+v, want version 6", u)
			}
		})
	}
}

func TestUnknownTypeIsAppliedAsSnapshot(t *testing.T) {
	p := NewProjection()
	if got := p.Apply(body(t, "e9", "identity.user.merged", ada)); got != Applied {
		t.Errorf("unknown type = %s, want applied (every event is a full snapshot)", got)
	}
}

func TestUnparsableBodiesAreRejected(t *testing.T) {
	p := NewProjection()
	noVersion := ada
	noVersion.Version = 0
	for name, msg := range map[string][]byte{
		"not json":              []byte("{"),
		"unknown schemaVersion": []byte(`{"id":"e1","type":"identity.user.updated","schemaVersion":2,"occurredAt":"2026-09-19T12:00:00Z","user":{"id":"u","email":"a@example.com","name":"A","active":true,"version":1}}`),
		"missing user id":       body(t, "e2", "identity.user.updated", UserSnapshot{Email: "a@example.com", Version: 1}),
		"version below one":     body(t, "e3", "identity.user.updated", noVersion),
		// A higher-version body without "active" must not decode as
		// active=false and deactivate the user (Codex review, PR #12).
		"missing active":      []byte(`{"id":"e5","type":"identity.user.updated","schemaVersion":1,"occurredAt":"2026-09-19T12:00:00Z","user":{"id":"u","email":"a@example.com","name":"A","version":9}}`),
		"missing occurredAt":  []byte(`{"id":"e6","type":"identity.user.updated","schemaVersion":1,"user":{"id":"u","email":"a@example.com","name":"A","active":true,"version":1}}`),
		"type outside family": body(t, "e7", "user.updated", ada),
		// null passes a presence check but not a type check; a plain bool
		// would decode it as false (Codex review, PR #12).
		"null active":    []byte(`{"id":"e8","type":"identity.user.updated","schemaVersion":1,"occurredAt":"2026-09-19T12:00:00Z","user":{"id":"u","email":"a@example.com","name":"A","active":null,"version":9}}`),
		"version string": []byte(`{"id":"e9","type":"identity.user.updated","schemaVersion":1,"occurredAt":"2026-09-19T12:00:00Z","user":{"id":"u","email":"a@example.com","name":"A","active":true,"version":"9"}}`),
	} {
		if got := p.Apply(msg); got != Rejected {
			t.Errorf("%s = %s, want rejected", name, got)
		}
	}
	if len(p.Users()) != 0 || p.Stats().Rejected != 9 {
		t.Errorf("rejected bodies must not touch the projection: users=%v stats=%+v", p.Users(), p.Stats())
	}
}

// Replay: a snapshot at the version already held is stale, a user the
// projection lacks is created — so replay is safe to repeat.
func TestReplayIsIdempotent(t *testing.T) {
	p := NewProjection()
	bob := UserSnapshot{ID: "b", Email: "bob@example.com", Name: "Bob", Active: true, Version: 2}
	p.Apply(body(t, "s1", "identity.user.snapshot", ada))
	first := []Outcome{p.Apply(body(t, "s2", "identity.user.snapshot", ada)), p.Apply(body(t, "s3", "identity.user.snapshot", bob))}
	if first[0] != Stale || first[1] != Applied {
		t.Errorf("replay outcomes = %v, want [stale applied]", first)
	}
	if len(p.Users()) != 2 {
		t.Errorf("users = %v, want ada and bob", p.Users())
	}
}
