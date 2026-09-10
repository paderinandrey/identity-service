-- +goose Up
ALTER TABLE users ADD COLUMN version bigint NOT NULL DEFAULT 1;
COMMENT ON COLUMN users.version IS 'Profile version, incremented on every profile/active change; lets consumers drop stale updates';

CREATE TABLE user_events_outbox (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type   text NOT NULL,
    payload      jsonb NOT NULL,
    attempts     integer NOT NULL DEFAULT 0,
    last_error   text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);

CREATE INDEX user_events_outbox_unpublished_idx
    ON user_events_outbox (created_at) WHERE published_at IS NULL;

COMMENT ON TABLE user_events_outbox IS 'Transactional outbox: user-change events awaiting publication to RabbitMQ';
COMMENT ON COLUMN user_events_outbox.id IS 'Event UUID; published as the AMQP message id';
COMMENT ON COLUMN user_events_outbox.event_type IS 'Event type, e.g. identity.user.updated';
COMMENT ON COLUMN user_events_outbox.payload IS 'Event body as delivered to consumers';
COMMENT ON COLUMN user_events_outbox.attempts IS 'Publish attempts counter';
COMMENT ON COLUMN user_events_outbox.last_error IS 'Last publish error, if any';
COMMENT ON COLUMN user_events_outbox.created_at IS 'When the event was recorded (with the user change transaction)';
COMMENT ON COLUMN user_events_outbox.published_at IS 'Broker publish timestamp; NULL while pending';

-- +goose Down
DROP TABLE user_events_outbox;
ALTER TABLE users DROP COLUMN version;
