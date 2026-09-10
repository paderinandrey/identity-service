package events

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	pollInterval   = time.Second
	batchSize      = 100
	maxBackoff     = 30 * time.Second
	publishTimeout = 5 * time.Second
)

// Relay drains the transactional outbox into a durable topic exchange.
// It reconnects with bounded backoff; the outbox preserves events across
// broker outages and service restarts. Run one instance per deployment to
// keep per-user ordering (SKIP LOCKED keeps extra instances safe, only
// ordering weakens — payload versions still fence stale updates).
type Relay struct {
	pool     *pgxpool.Pool
	url      string
	exchange string
	logger   *slog.Logger

	conn    *amqp.Connection
	channel *amqp.Channel
}

// NewRelay builds an outbox publisher.
func NewRelay(pool *pgxpool.Pool, url, exchange string, logger *slog.Logger) *Relay {
	return &Relay{pool: pool, url: url, exchange: exchange, logger: logger}
}

// Run publishes pending events until ctx is done.
func (r *Relay) Run(ctx context.Context) {
	backoff := pollInterval
	for {
		published, err := r.drainOnce(ctx)
		switch {
		case ctx.Err() != nil:
			r.close()
			return
		case err != nil:
			r.logger.Warn("outbox publication failed, retrying", "error", err, "backoff", backoff.String())
			r.close() // force reconnect on the next round
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, maxBackoff)
		case published == batchSize:
			backoff = pollInterval // full batch: keep draining immediately
		default:
			backoff = pollInterval
			if !sleepCtx(ctx, pollInterval) {
				r.close()
				return
			}
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (r *Relay) ensureChannel() error {
	if r.channel != nil && !r.channel.IsClosed() {
		return nil
	}
	r.close()
	conn, err := amqp.Dial(r.url)
	if err != nil {
		return fmt.Errorf("amqp dial: %w", err)
	}
	channel, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("amqp channel: %w", err)
	}
	if err := channel.Confirm(false); err != nil {
		_ = conn.Close()
		return fmt.Errorf("amqp confirm mode: %w", err)
	}
	if err := channel.ExchangeDeclare(r.exchange, "topic", true, false, false, false, nil); err != nil {
		_ = conn.Close()
		return fmt.Errorf("declare exchange: %w", err)
	}
	r.conn, r.channel = conn, channel
	r.logger.Info("connected to RabbitMQ", "exchange", r.exchange)
	return nil
}

func (r *Relay) close() {
	if r.channel != nil {
		_ = r.channel.Close()
		r.channel = nil
	}
	if r.conn != nil {
		_ = r.conn.Close()
		r.conn = nil
	}
}

type outboxRow struct {
	id        string
	eventType string
	payload   []byte
}

// drainOnce publishes one batch; returns how many events were published.
func (r *Relay) drainOnce(ctx context.Context) (int, error) {
	if err := r.ensureChannel(); err != nil {
		return 0, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx,
		`SELECT id, event_type, payload FROM user_events_outbox
		 WHERE published_at IS NULL
		 ORDER BY created_at, id
		 LIMIT $1
		 FOR UPDATE SKIP LOCKED`, batchSize)
	if err != nil {
		return 0, err
	}
	batch := []outboxRow{}
	for rows.Next() {
		var row outboxRow
		if err := rows.Scan(&row.id, &row.eventType, &row.payload); err != nil {
			rows.Close()
			return 0, err
		}
		batch = append(batch, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(batch) == 0 {
		return 0, tx.Commit(ctx)
	}

	published := 0
	var publishErr error
	for _, row := range batch {
		if publishErr = r.publish(ctx, row); publishErr != nil {
			if err := markFailed(ctx, tx, row.id, publishErr); err != nil {
				return published, err
			}
			break
		}
		if _, err := tx.Exec(ctx,
			"UPDATE user_events_outbox SET published_at = now() WHERE id = $1", row.id); err != nil {
			return published, err
		}
		published++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	if publishErr != nil {
		return published, publishErr
	}
	if published > 0 {
		r.logger.Info("published user events", "count", published)
	}
	return published, nil
}

func markFailed(ctx context.Context, tx pgx.Tx, id string, cause error) error {
	_, err := tx.Exec(ctx,
		"UPDATE user_events_outbox SET attempts = attempts + 1, last_error = $2 WHERE id = $1",
		id, cause.Error())
	return err
}

// publish sends one event with publisher confirmation.
func (r *Relay) publish(ctx context.Context, row outboxRow) error {
	pubCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()

	confirmation, err := r.channel.PublishWithDeferredConfirmWithContext(pubCtx,
		r.exchange,
		strings.TrimPrefix(row.eventType, "identity."), // routing key, e.g. user.updated
		false, false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			MessageId:    row.id,
			Type:         row.eventType,
			Timestamp:    time.Now(),
			Body:         row.payload,
		})
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	ok, err := confirmation.WaitContext(pubCtx)
	if err != nil {
		return fmt.Errorf("confirm: %w", err)
	}
	if !ok {
		return fmt.Errorf("broker nacked message %s", row.id)
	}
	return nil
}
