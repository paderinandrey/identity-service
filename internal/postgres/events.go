package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/xometry-europe-gmbh/identity-service/internal/events"
	"github.com/xometry-europe-gmbh/identity-service/internal/identity"
)

// recordUserEvent writes an outbox event within the change transaction.
// The generated row id is injected into the payload as the event id.
func recordUserEvent(ctx context.Context, tx pgx.Tx, eventType string, user *identity.User) error {
	body, err := json.Marshal(events.NewPayload(eventType, user))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO user_events_outbox (id, event_type, payload)
		 SELECT uid, $1, jsonb_set($2::jsonb, '{id}', to_jsonb(uid::text))
		 FROM (SELECT gen_random_uuid() AS uid) t`, eventType, body)
	return err
}

// EnqueueSnapshots writes a snapshot event for every user, in batches;
// used by the replay CLI to (re)build downstream projections.
func (s *Store) EnqueueSnapshots(ctx context.Context, batchSize int) (int, error) {
	total := 0
	for offset := 0; ; {
		users, _, err := s.ListUsersPage(ctx, offset, batchSize)
		if err != nil {
			return total, err
		}
		if len(users) == 0 {
			return total, nil
		}
		err = s.inTx(ctx, func(tx pgx.Tx) error {
			for _, u := range users {
				if err := recordUserEvent(ctx, tx, events.TypeSnapshot, u); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return total, err
		}
		total += len(users)
		offset += len(users)
	}
}
