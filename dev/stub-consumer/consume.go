package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Consumer owns its queue: a durable queue named after the service, bound
// to the identity exchange with user.# — the topology the contract asks
// every consumer to own. Reconnects with backoff when the broker goes
// away; the projection survives in memory across reconnects.
type Consumer struct {
	url, exchange, queue string
	projection           *Projection
	logger               *slog.Logger
}

// Run consumes until ctx is cancelled.
func (c *Consumer) Run(ctx context.Context) {
	backoff := time.Second
	for {
		err := c.consumeOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		c.logger.Warn("consumer disconnected, reconnecting", "error", err, "backoff", backoff.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (c *Consumer) consumeOnce(ctx context.Context) error {
	conn, err := amqp.Dial(c.url)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer func() { _ = ch.Close() }()

	// Declaring the exchange with the producer's parameters is idempotent
	// and lets the consumer start before the producer ever published.
	if err := ch.ExchangeDeclare(c.exchange, "topic", true, false, false, false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(c.queue, true, false, false, false, nil); err != nil {
		return err
	}
	if err := ch.QueueBind(c.queue, "user.#", c.exchange, false, nil); err != nil {
		return err
	}
	if err := ch.Qos(16, 0, false); err != nil {
		return err
	}
	deliveries, err := ch.Consume(c.queue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}
	c.logger.Info("consuming", "queue", c.queue, "exchange", c.exchange)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case d, ok := <-deliveries:
			if !ok {
				return errors.New("delivery channel closed")
			}
			outcome := c.projection.Apply(d.Body)
			c.logger.Info("event", "message_id", d.MessageId, "type", d.Type, "outcome", outcome)
			var ackErr error
			if outcome == Rejected {
				// Dead-letter, never requeue: the body itself is the problem.
				ackErr = d.Nack(false, false)
			} else {
				ackErr = d.Ack(false)
			}
			if ackErr != nil {
				return ackErr
			}
		}
	}
}
