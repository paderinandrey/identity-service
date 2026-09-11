package events

import (
	"testing"
	"time"

	"github.com/paderinandrey/identity-service/internal/identity"
)

func TestTypeForActivation(t *testing.T) {
	if TypeForActivation(false) != TypeDeactivated || TypeForActivation(true) != TypeReactivated {
		t.Error("activation type mapping broken")
	}
}

func TestNewPayload(t *testing.T) {
	u := &identity.User{ID: "u1", Email: "e@example.com", Name: "N", Active: true, Version: 7}
	p := NewPayload(TypeUpdated, u)
	if p.Type != TypeUpdated || p.SchemaVersion != SchemaVersion || p.User.Version != 7 {
		t.Errorf("payload = %+v", p)
	}
	if time.Since(p.OccurredAt) > time.Minute || p.OccurredAt.Location() != time.UTC {
		t.Errorf("occurredAt = %v", p.OccurredAt)
	}
}
