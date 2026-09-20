package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	pollInterval   = time.Second
	batchSize      = 25
	publishTimeout = 5 * time.Second
	// leaseDuration comfortably exceeds the worst case of a batch
	// (batchSize × publishTimeout); a replica that dies mid-batch releases
	// its rows when the lease expires.
	leaseDuration = 5 * time.Minute
	// settleTimeout bounds recording a batch's outcomes once the service
	// context is gone: the leases must be released and confirmed publishes
	// marked even during shutdown, or the rows sit for a full lease and
	// confirmed events are republished (Codex review, PR #6).
	settleTimeout     = 10 * time.Second
	loopBackoffMax    = 30 * time.Second
	retentionInterval = time.Hour
	retentionBatch    = 1000
)

// RetryPolicy bounds how a failing event is retried before quarantine.
type RetryPolicy struct {
	MaxAttempts int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

// DefaultRetryPolicy retries ten times with exponential backoff capped at
// five minutes — roughly half an hour before a row is parked.
var DefaultRetryPolicy = RetryPolicy{MaxAttempts: 10, BaseBackoff: time.Second, MaxBackoff: 5 * time.Minute}

// backoff returns the not-before delay after the given number of attempts.
func (p RetryPolicy) backoff(attempts int) time.Duration {
	d := p.BaseBackoff
	for i := 1; i < attempts && d < p.MaxBackoff; i++ {
		d *= 2
	}
	return min(d, p.MaxBackoff)
}

// Metrics observes relay activity; a nil Metrics disables instrumentation.
type Metrics interface {
	Published(n int)
	PublishError()
	Unroutable()
	PendingSet(n int)
	QuarantinedSet(n int)
	OldestPendingAgeSet(seconds float64)
}

// Relay drains the transactional outbox into the broker.
//
// Each round is three short steps: claim a batch under a lease (one
// transaction), publish outside any transaction, settle the outcome (one
// transaction). A row is claimed only when it is the earliest pending
// event of its user, so events of one user leave in order however many
// replicas run. Failures back off and eventually quarantine; an event no
// queue accepts waits for a consumer; published rows are trimmed after
// the retention period.
type Relay struct {
	pool      *pgxpool.Pool
	logger    *slog.Logger
	publisher Publisher
	metrics   Metrics
	replica   string
	retry     RetryPolicy
	retention time.Duration

	lastRetention time.Time
}

// NewRelay builds an outbox publisher for the AMQP broker at url.
func NewRelay(pool *pgxpool.Pool, url, exchange string, logger *slog.Logger) *Relay {
	return &Relay{
		pool:      pool,
		logger:    logger,
		publisher: newAMQPPublisher(url, exchange, logger),
		replica:   replicaID(),
		retry:     DefaultRetryPolicy,
	}
}

// SetMetrics attaches instrumentation; call before Run.
func (r *Relay) SetMetrics(m Metrics) { r.metrics = m }

// SetPublisher replaces the broker client; used by tests.
func (r *Relay) SetPublisher(p Publisher) { r.publisher = p }

// SetRetryPolicy replaces the default retry budget.
func (r *Relay) SetRetryPolicy(p RetryPolicy) { r.retry = p }

// SetRetention enables trimming of published rows older than d.
func (r *Relay) SetRetention(d time.Duration) { r.retention = d }

func replicaID() string {
	host, _ := os.Hostname()
	var b [4]byte
	_, _ = rand.Read(b[:])
	return host + "/" + hex.EncodeToString(b[:])
}

// Run publishes pending events until ctx is done.
func (r *Relay) Run(ctx context.Context) {
	backoff := pollInterval
	for {
		// Gauges first, before touching the broker: a broken broker is
		// exactly when the queue depth matters.
		r.observe(ctx)
		r.maybeTrim(ctx)

		published, err := r.drainOnce(ctx)
		switch {
		case ctx.Err() != nil:
			r.publisher.Close()
			return
		case err != nil:
			r.logger.Warn("outbox publication failed, retrying", "error", err, "backoff", backoff.String())
			r.publisher.Close() // force reconnect on the next round
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, loopBackoffMax)
		case published == batchSize:
			backoff = pollInterval // full batch: keep draining immediately
		default:
			backoff = pollInterval
			if !sleepCtx(ctx, pollInterval) {
				r.publisher.Close()
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

type outboxRow struct {
	id        string
	eventType string
	payload   []byte
	attempts  int
}

// claim leases the next batch: the earliest pending rows, at most one per
// user (the head of that user's line), not leased by anyone else and not
// backing off. One short transaction; the lease is what other replicas
// see, so nothing is held open while the broker is talked to.
//
// The head of a user's line is the lowest user_version, not the oldest
// created_at: created_at is the transaction start time, so two events
// written in one transaction share it and a transaction that started
// earlier but took the user row lock later carries the higher version
// with the lower timestamp (Codex review, PR #6).
func (r *Relay) claim(ctx context.Context) ([]outboxRow, error) {
	// Rows written by a replica of the previous release carry the user
	// only in the payload (user_id NULL). Repair pending rows so the
	// per-user index applies to them; bounded by the pending set, a
	// no-op once the old writers are gone. A row such a replica commits
	// between this statement and the claim is still ordered correctly:
	// the predicate below falls back to the payload for NULL columns.
	if _, err := r.pool.Exec(ctx,
		`UPDATE user_events_outbox
		 SET user_id = (payload -> 'user' ->> 'id')::uuid,
		     user_version = (payload -> 'user' ->> 'version')::bigint
		 WHERE (user_id IS NULL OR user_version IS NULL) AND published_at IS NULL
		   AND payload -> 'user' ->> 'id' IS NOT NULL`); err != nil {
		return nil, fmt.Errorf("repair outbox user ids: %w", err)
	}
	rows, err := r.pool.Query(ctx,
		`WITH picked AS (
		   SELECT o.id FROM user_events_outbox o
		   WHERE o.published_at IS NULL AND o.quarantined_at IS NULL
		     AND (o.lease_until IS NULL OR o.lease_until < now())
		     AND NOT EXISTS (
		       SELECT 1 FROM user_events_outbox p
		       WHERE COALESCE(p.user_id, (p.payload -> 'user' ->> 'id')::uuid)
		             = COALESCE(o.user_id, (o.payload -> 'user' ->> 'id')::uuid)
		         AND p.published_at IS NULL AND p.quarantined_at IS NULL
		         AND (COALESCE(p.user_version, (p.payload -> 'user' ->> 'version')::bigint), p.created_at, p.id)
		           < (COALESCE(o.user_version, (o.payload -> 'user' ->> 'version')::bigint), o.created_at, o.id))
		   ORDER BY o.created_at, o.id
		   LIMIT $1
		   FOR UPDATE SKIP LOCKED)
		 UPDATE user_events_outbox u
		 SET lease_until = now() + $2::interval, leased_by = $3
		 FROM picked WHERE u.id = picked.id
		 RETURNING u.id, u.event_type, u.payload, u.attempts`,
		batchSize, leaseDuration.String(), r.replica)
	if err != nil {
		return nil, fmt.Errorf("claim outbox rows: %w", err)
	}
	defer rows.Close()
	batch := []outboxRow{}
	for rows.Next() {
		var row outboxRow
		if err := rows.Scan(&row.id, &row.eventType, &row.payload, &row.attempts); err != nil {
			return nil, err
		}
		batch = append(batch, row)
	}
	return batch, rows.Err()
}

type outcome struct {
	row outboxRow
	// err: nil — published; ErrUnroutable — waiting for a consumer;
	// ErrTransport — the broker, not the message, failed (lease released,
	// no attempt spent); anything else — a failure of this message.
	err error
	// tried is false for rows the batch never reached; their lease is
	// simply released.
	tried bool
}

// drainOnce publishes one batch; returns how many events were published.
func (r *Relay) drainOnce(ctx context.Context) (int, error) {
	batch, err := r.claim(ctx)
	if err != nil {
		return 0, err
	}
	if len(batch) == 0 {
		return 0, nil
	}

	outcomes := make([]outcome, len(batch))
	var stop error
	for i, row := range batch {
		outcomes[i].row = row
		if stop != nil {
			continue // released untried below
		}
		pubCtx, cancel := context.WithTimeout(ctx, publishTimeout)
		err := r.publisher.Publish(pubCtx, Message{
			ID:         row.id,
			Type:       row.eventType,
			RoutingKey: strings.TrimPrefix(row.eventType, "identity."),
			Body:       row.payload,
		})
		cancel()
		outcomes[i].tried = true
		outcomes[i].err = err
		if errors.Is(err, ErrTransport) {
			// The broker is gone: stop the batch, release the rest,
			// reconnect after the relay-level backoff. No row is charged.
			stop = err
		}
	}

	published, err := r.settle(ctx, outcomes)
	if err != nil {
		return published, err
	}
	if published > 0 {
		r.logger.Info("published user events", "count", published)
	}
	return published, stop
}

// settle records every outcome of a batch in one short transaction. It
// runs on a context detached from the service's cancellation: a SIGTERM
// that interrupts the batch must still release the leases and mark what
// the broker already confirmed.
func (r *Relay) settle(ctx context.Context, outcomes []outcome) (int, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	published, failed, unroutable := 0, 0, 0
	for _, o := range outcomes {
		switch {
		case !o.tried, errors.Is(o.err, ErrTransport):
			// Not the message's fault: back to the queue untouched. A
			// broker outage must never quarantine valid events (Codex
			// review, PR #6).
			err = releaseLease(ctx, tx, o.row.id)
		case o.err == nil:
			err = markPublished(ctx, tx, o.row.id)
			published++
		case errors.Is(o.err, ErrUnroutable):
			// Not a failure and not an attempt: the event waits for a
			// consumer to bind a queue, at the longest backoff.
			err = deferRow(ctx, tx, o.row.id, o.err, r.retry.MaxBackoff)
			unroutable++
		default:
			attempts := o.row.attempts + 1
			if attempts >= r.retry.MaxAttempts {
				err = quarantine(ctx, tx, o.row.id, attempts, o.err)
				r.logger.Error("user event quarantined after repeated failures",
					"event_id", o.row.id, "attempts", attempts, "error", o.err)
			} else {
				err = markFailed(ctx, tx, o.row.id, attempts, o.err, r.retry.backoff(attempts))
			}
			failed++
		}
		if err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	if r.metrics != nil {
		r.metrics.Published(published)
		for range failed {
			r.metrics.PublishError()
		}
		for range unroutable {
			r.metrics.Unroutable()
		}
	}
	if unroutable > 0 {
		r.logger.Warn("user events have no bound queue; waiting for a consumer", "count", unroutable)
	}
	return published, nil
}

func markPublished(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx,
		`UPDATE user_events_outbox SET published_at = now(), lease_until = NULL, leased_by = NULL
		 WHERE id = $1 AND published_at IS NULL`, id)
	return err
}

func releaseLease(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx,
		"UPDATE user_events_outbox SET lease_until = NULL, leased_by = NULL WHERE id = $1", id)
	return err
}

func markFailed(ctx context.Context, tx pgx.Tx, id string, attempts int, cause error, backoff time.Duration) error {
	_, err := tx.Exec(ctx,
		`UPDATE user_events_outbox
		 SET attempts = $2, last_error = $3, leased_by = NULL, lease_until = now() + $4::interval
		 WHERE id = $1`, id, attempts, cause.Error(), backoff.String())
	return err
}

func deferRow(ctx context.Context, tx pgx.Tx, id string, cause error, backoff time.Duration) error {
	_, err := tx.Exec(ctx,
		`UPDATE user_events_outbox
		 SET last_error = $2, leased_by = NULL, lease_until = now() + $3::interval
		 WHERE id = $1`, id, cause.Error(), backoff.String())
	return err
}

func quarantine(ctx context.Context, tx pgx.Tx, id string, attempts int, cause error) error {
	_, err := tx.Exec(ctx,
		`UPDATE user_events_outbox
		 SET attempts = $2, last_error = $3, leased_by = NULL, lease_until = NULL, quarantined_at = now()
		 WHERE id = $1`, id, attempts, cause.Error())
	return err
}

// observe refreshes the queue gauges. It runs every loop, broker or no
// broker: a growing backlog is the signal an outage produces.
func (r *Relay) observe(ctx context.Context) {
	if r.metrics == nil {
		return
	}
	// Three scalar subqueries, each answered by a partial index over its
	// own set, so retained published history is never scanned for a
	// gauge refresh (Codex review, PR #6).
	var pending, quarantined int
	var oldest float64
	err := r.pool.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM user_events_outbox WHERE published_at IS NULL AND quarantined_at IS NULL),
		        (SELECT count(*) FROM user_events_outbox WHERE quarantined_at IS NOT NULL),
		        COALESCE((SELECT EXTRACT(EPOCH FROM now() - min(created_at)) FROM user_events_outbox
		                  WHERE published_at IS NULL AND quarantined_at IS NULL), 0)`).Scan(&pending, &quarantined, &oldest)
	if err != nil {
		r.logger.Warn("outbox gauges unavailable", "error", err)
		return
	}
	r.metrics.PendingSet(pending)
	r.metrics.QuarantinedSet(quarantined)
	r.metrics.OldestPendingAgeSet(oldest)
}

// maybeTrim deletes published rows older than the retention period, in
// bounded batches, at most once per retentionInterval.
func (r *Relay) maybeTrim(ctx context.Context) {
	if r.retention <= 0 || time.Since(r.lastRetention) < retentionInterval {
		return
	}
	r.lastRetention = time.Now()
	total := 0
	for {
		tag, err := r.pool.Exec(ctx,
			`DELETE FROM user_events_outbox WHERE id IN (
			   SELECT id FROM user_events_outbox
			   WHERE published_at IS NOT NULL AND published_at < now() - $1::interval
			   LIMIT $2)`, r.retention.String(), retentionBatch)
		if err != nil {
			r.logger.Warn("outbox retention failed", "error", err)
			return
		}
		total += int(tag.RowsAffected())
		if tag.RowsAffected() < retentionBatch {
			break
		}
	}
	if total > 0 {
		r.logger.Info("trimmed published user events", "count", total, "older_than", r.retention.String())
	}
}
