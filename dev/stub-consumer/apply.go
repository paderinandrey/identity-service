// Reference consumer of identity-service user events. It follows
// docs/events/user-events.md line by line so that a projection in another
// language (GSH/DFM are Ruby) can be written against the same rules:
// deduplicate by event id, apply a snapshot only when its version is
// greater than the one already applied, treat every type as a full
// snapshot, reject only what cannot be parsed.
//
// The projection lives in memory: this is a reference for behaviour and a
// stand fixture, not a library.
package main

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"
)

// Event mirrors docs/events/user-event.schema.json.
type Event struct {
	ID            string       `json:"id"`
	Type          string       `json:"type"`
	SchemaVersion int          `json:"schemaVersion"`
	OccurredAt    time.Time    `json:"occurredAt"`
	User          UserSnapshot `json:"user"`
}

// UserSnapshot is the full user state every event carries.
type UserSnapshot struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Active  bool   `json:"active"`
	Version int64  `json:"version"`
}

// Outcome of applying one message; each maps to an ack decision.
type Outcome string

const (
	// Applied: the projection now holds this snapshot. Ack.
	Applied Outcome = "applied"
	// Duplicate: this event id was processed before. Ack, change nothing.
	Duplicate Outcome = "duplicate"
	// Stale: the version is not greater than the one applied. Ack, change nothing.
	Stale Outcome = "stale"
	// Rejected: the body cannot be parsed or is from an unknown schema.
	// Nack without requeue (dead-letter); retrying cannot help.
	Rejected Outcome = "rejected"
)

// Stats counts outcomes since start.
type Stats struct {
	Applied    int `json:"applied"`
	Duplicates int `json:"duplicates"`
	Stale      int `json:"stale"`
	Rejected   int `json:"rejected"`
}

// Projection is the local user table plus the inbox of processed ids.
// One mutex guards both, which is what "check the version and write the
// row atomically" means here; in SQL it is the conditional UPDATE under
// the row lock shown in the contract document.
type Projection struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	users map[string]UserSnapshot
	stats Stats
}

// NewProjection returns an empty projection.
func NewProjection() *Projection {
	return &Projection{seen: map[string]struct{}{}, users: map[string]UserSnapshot{}}
}

const schemaVersion = 1

var errInvalid = errors.New("invalid event")

// parse decodes and validates a body against the contract's invariants.
// The type is deliberately not validated: an unknown type is still a
// full snapshot and is applied like any other (compatibility rule).
func parse(body []byte) (Event, error) {
	var ev Event
	if err := json.Unmarshal(body, &ev); err != nil {
		return ev, err
	}
	switch {
	case ev.SchemaVersion != schemaVersion:
		return ev, errors.Join(errInvalid, errors.New("unknown schemaVersion"))
	case ev.ID == "", ev.User.ID == "", ev.User.Email == "":
		return ev, errors.Join(errInvalid, errors.New("missing id, user.id or user.email"))
	case ev.User.Version < 1:
		return ev, errors.Join(errInvalid, errors.New("user.version must be >= 1"))
	}
	return ev, nil
}

// Apply processes one message body and reports what happened.
func (p *Projection) Apply(body []byte) Outcome {
	ev, err := parse(body)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.stats.Rejected++
		return Rejected
	}
	if _, dup := p.seen[ev.ID]; dup {
		p.stats.Duplicates++
		return Duplicate
	}
	p.seen[ev.ID] = struct{}{}
	if cur, ok := p.users[ev.User.ID]; ok && cur.Version >= ev.User.Version {
		p.stats.Stale++
		return Stale
	}
	p.users[ev.User.ID] = ev.User
	p.stats.Applied++
	return Applied
}

// User returns the projected user, if any.
func (p *Projection) User(id string) (UserSnapshot, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	u, ok := p.users[id]
	return u, ok
}

// Users returns every projected user, ordered by id for stable output.
func (p *Projection) Users() []UserSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]UserSnapshot, 0, len(p.users))
	for _, u := range p.users {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Stats returns the outcome counters.
func (p *Projection) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}
