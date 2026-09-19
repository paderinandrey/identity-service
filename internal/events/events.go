// Package events defines the user-change event contract and the outbox
// relay that publishes events to RabbitMQ.
package events

import (
	"time"

	"github.com/paderinandrey/identity-service/internal/identity"
)

// Event types (also used as AMQP routing keys without the "identity." prefix).
const (
	TypeCreated     = "identity.user.created"
	TypeUpdated     = "identity.user.updated"
	TypeDeactivated = "identity.user.deactivated"
	TypeReactivated = "identity.user.reactivated"
	TypeSnapshot    = "identity.user.snapshot"
)

// SchemaVersion of the event payload.
const SchemaVersion = 1

// UserSnapshot is the user state carried by every event.
type UserSnapshot struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Active  bool   `json:"active"`
	Version int64  `json:"version"`
}

// Payload is the event body as delivered to consumers. The ID equals the
// outbox row id and the AMQP message id.
type Payload struct {
	ID            string       `json:"id"`
	Type          string       `json:"type"`
	SchemaVersion int          `json:"schemaVersion"`
	OccurredAt    time.Time    `json:"occurredAt"`
	User          UserSnapshot `json:"user"`
}

// now is the clock behind OccurredAt; tests pin it so that the published
// examples in docs/events are reproducible.
var now = time.Now

// NewPayload builds an event body for the user; ID is filled by the
// outbox insert.
func NewPayload(eventType string, u *identity.User) Payload {
	return Payload{
		Type:          eventType,
		SchemaVersion: SchemaVersion,
		OccurredAt:    now().UTC(),
		User: UserSnapshot{
			ID:      u.ID,
			Email:   u.Email,
			Name:    u.Name,
			Active:  u.Active,
			Version: u.Version,
		},
	}
}

// TypeForActivation picks the event type for an active-flag change.
func TypeForActivation(nowActive bool) string {
	if nowActive {
		return TypeReactivated
	}
	return TypeDeactivated
}
