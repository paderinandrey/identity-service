package events_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/paderinandrey/identity-service/internal/events"
	"github.com/paderinandrey/identity-service/internal/postgres"
)

const (
	adminDBURL = "postgres://identity:identity@localhost:5433/identity_development?sslmode=disable"
	testDBName = "identity_events_test"
	testDBURL  = "postgres://identity:identity@localhost:5433/" + testDBName + "?sslmode=disable"
	amqpURL    = "amqp://identity:identity@localhost:5673/"
)

type env struct {
	pool     *pgxpool.Pool
	store    *postgres.Store
	exchange string
	queue    string
	channel  *amqp.Channel
	metrics  *fakeRelayMetrics
}

// newEnv provisions a database, an exchange and a bound queue.
func newEnv(t *testing.T) *env {
	e := newEnvUnbound(t)
	e.bindQueue(t)
	return e
}

// bindQueue declares the consumer side: an exclusive queue bound to every
// user event — the topology consumers own in production.
func (e *env) bindQueue(t *testing.T) {
	t.Helper()
	queue, err := e.channel.QueueDeclare("", false, true, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.queue = queue.Name
	t.Cleanup(func() { _, _ = e.channel.QueueDelete(e.queue, false, false, false) })
	if err := e.channel.QueueBind(e.queue, "user.#", e.exchange, false, nil); err != nil {
		t.Fatal(err)
	}
}

// newEnvUnbound provisions a database and an exchange with no queue: the
// state of the world before any consumer has deployed.
func newEnvUnbound(t *testing.T) *env {
	t.Helper()
	ctx := t.Context()

	admin, err := pgxpool.New(ctx, adminDBURL)
	if err == nil {
		err = admin.Ping(ctx)
	}
	if err != nil {
		t.Skipf("PostgreSQL from docker-compose is not available: %v (run `mise run up`)", err)
	}
	t.Cleanup(admin.Close)
	for _, q := range []string{
		"DROP DATABASE IF EXISTS " + testDBName + " WITH (FORCE)",
		"CREATE DATABASE " + testDBName,
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+testDBName+" WITH (FORCE)")
	})
	if err := postgres.Migrate(ctx, testDBURL); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.Connect(ctx, testDBURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Skipf("RabbitMQ from docker-compose is not available: %v (run `mise run up`)", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	channel, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}

	e := &env{
		pool:     pool,
		store:    postgres.NewStore(pool),
		exchange: fmt.Sprintf("identity.events.test.%d", time.Now().UnixNano()),
		channel:  channel,
	}
	if err := channel.ExchangeDeclare(e.exchange, "topic", true, false, false, false, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = channel.ExchangeDelete(e.exchange, false, false) })
	return e
}

// outboxRow reads the bookkeeping columns of one event.
func (e *env) outboxRow(t *testing.T, id string) (attempts int, published, quarantined bool, lastError string) {
	t.Helper()
	var pub, quar *time.Time
	var errText *string
	if err := e.pool.QueryRow(t.Context(),
		"SELECT attempts, published_at, quarantined_at, last_error FROM user_events_outbox WHERE id = $1", id).
		Scan(&attempts, &pub, &quar, &errText); err != nil {
		t.Fatal(err)
	}
	if errText != nil {
		lastError = *errText
	}
	return attempts, pub != nil, quar != nil, lastError
}

// eventIDs returns outbox ids in creation order.
func (e *env) eventIDs(t *testing.T) []string {
	t.Helper()
	rows, err := e.pool.Query(t.Context(), "SELECT id FROM user_events_outbox ORDER BY created_at, id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// fakePublisher records what it was asked to publish and fails the ids
// in failIDs; the relay's retry and quarantine logic is exercised against
// the real database without needing the broker to misbehave.
type fakePublisher struct {
	mu        sync.Mutex
	failIDs   map[string]bool
	published []string
}

func (p *fakePublisher) Publish(_ context.Context, m events.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failIDs[m.ID] {
		return fmt.Errorf("simulated broker failure for %s", m.ID)
	}
	p.published = append(p.published, m.ID)
	return nil
}

func (p *fakePublisher) Close() {}

func (p *fakePublisher) got() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.published...)
}

func (p *fakePublisher) stopFailing() {
	p.mu.Lock()
	p.failIDs = map[string]bool{}
	p.mu.Unlock()
}

var fastRetry = events.RetryPolicy{MaxAttempts: 3, BaseBackoff: 10 * time.Millisecond, MaxBackoff: 100 * time.Millisecond}

func contains(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

type fakeRelayMetrics struct {
	mu          sync.Mutex
	published   int
	errors      int
	unroutable  int
	pending     int
	quarantined int
	oldestAge   float64
}

func (m *fakeRelayMetrics) Published(n int) { m.mu.Lock(); m.published += n; m.mu.Unlock() }
func (m *fakeRelayMetrics) PublishError()   { m.mu.Lock(); m.errors++; m.mu.Unlock() }
func (m *fakeRelayMetrics) Unroutable()     { m.mu.Lock(); m.unroutable++; m.mu.Unlock() }
func (m *fakeRelayMetrics) PendingSet(n int) {
	m.mu.Lock()
	m.pending = n
	m.mu.Unlock()
}
func (m *fakeRelayMetrics) QuarantinedSet(n int) {
	m.mu.Lock()
	m.quarantined = n
	m.mu.Unlock()
}
func (m *fakeRelayMetrics) OldestPendingAgeSet(seconds float64) {
	m.mu.Lock()
	m.oldestAge = seconds
	m.mu.Unlock()
}

func (m *fakeRelayMetrics) snapshot() (int, int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.published, m.errors, m.pending
}

func (m *fakeRelayMetrics) queue() (pending, quarantined int, oldestAge float64, unroutable int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pending, m.quarantined, m.oldestAge, m.unroutable
}

func (e *env) runRelay(t *testing.T, url string) (stop func()) {
	return e.runRelayWith(t, url, func(*events.Relay) {})
}

// runRelayWith starts a relay after applying tweak (publisher, retry
// policy, retention). Each call gets its own metrics unless one is set.
func (e *env) runRelayWith(t *testing.T, url string, tweak func(*events.Relay)) (stop func()) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	relay := events.NewRelay(e.pool, url, e.exchange, logger)
	if e.metrics == nil {
		e.metrics = &fakeRelayMetrics{}
	}
	relay.SetMetrics(e.metrics)
	tweak(relay)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		relay.Run(ctx)
		close(done)
	}()
	return func() {
		cancel()
		<-done
	}
}

func (e *env) consume(t *testing.T, n int, timeout time.Duration) []amqp.Delivery {
	t.Helper()
	deliveries, err := e.channel.Consume(e.queue, "", true, true, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := []amqp.Delivery{}
	deadline := time.After(timeout)
	for len(got) < n {
		select {
		case d := <-deliveries:
			got = append(got, d)
		case <-deadline:
			t.Fatalf("received %d/%d messages within %s", len(got), n, timeout)
		}
	}
	return got
}

func (e *env) unpublishedCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(t.Context(),
		"SELECT count(*) FROM user_events_outbox WHERE published_at IS NULL").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRelayDeliversWithProperties(t *testing.T) {
	e := newEnv(t)

	user, err := e.store.CreateUser(t.Context(), "relay@example.com", "Relay User", true)
	if err != nil {
		t.Fatal(err)
	}

	stop := e.runRelay(t, amqpURL)
	defer stop()

	msgs := e.consume(t, 1, 10*time.Second)
	m := msgs[0]
	if m.RoutingKey != "user.created" || m.Type != events.TypeCreated {
		t.Errorf("routing/type = %q/%q", m.RoutingKey, m.Type)
	}
	if m.DeliveryMode != amqp.Persistent || m.ContentType != "application/json" || m.MessageId == "" {
		t.Errorf("properties: mode=%d ct=%q id=%q", m.DeliveryMode, m.ContentType, m.MessageId)
	}
	var payload events.Payload
	if err := json.Unmarshal(m.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ID != m.MessageId || payload.User.ID != user.ID || payload.User.Email != "relay@example.com" {
		t.Errorf("payload = %+v", payload)
	}

	waitFor(t, 5*time.Second, func() bool { return e.unpublishedCount(t) == 0 })
	waitFor(t, 5*time.Second, func() bool {
		published, _, pending := e.metrics.snapshot()
		return published == 1 && pending == 0
	})
}

func TestRelayPreservesPerUserOrder(t *testing.T) {
	e := newEnv(t)

	user, err := e.store.CreateUser(t.Context(), "ordered@example.com", "V1", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.UpdateUser(t.Context(), user.ID, "ordered@example.com", "V2"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.SetActive(t.Context(), user.ID, false); err != nil {
		t.Fatal(err)
	}

	stop := e.runRelay(t, amqpURL)
	defer stop()

	msgs := e.consume(t, 3, 10*time.Second)
	var prev int64
	for i, m := range msgs {
		var payload events.Payload
		if err := json.Unmarshal(m.Body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.User.Version <= prev {
			t.Errorf("message %d: version %d after %d — order broken", i, payload.User.Version, prev)
		}
		prev = payload.User.Version
	}
}

func TestRelaySurvivesBrokerOutageAndRestart(t *testing.T) {
	e := newEnv(t)

	// Broker "down": relay points at a dead endpoint, events accumulate.
	if _, err := e.store.CreateUser(t.Context(), "outage@example.com", "Outage", true); err != nil {
		t.Fatal(err)
	}
	stopBroken := e.runRelay(t, "amqp://identity:identity@localhost:1/")
	time.Sleep(1500 * time.Millisecond)
	stopBroken()
	if e.unpublishedCount(t) != 1 {
		t.Fatalf("event must stay in outbox while broker is unreachable")
	}

	// "Restart" with a healthy broker: the backlog drains.
	stop := e.runRelay(t, amqpURL)
	defer stop()
	e.consume(t, 1, 10*time.Second)
	waitFor(t, 5*time.Second, func() bool { return e.unpublishedCount(t) == 0 })
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestMain(m *testing.M) {
	fmt.Fprintln(os.Stderr, "events integration tests use docker-compose PostgreSQL/RabbitMQ (run `mise run up`)")
	os.Exit(m.Run())
}

func TestTwoRelaysKeepPerUserOrder(t *testing.T) {
	// The spec promises per-user order for any number of relay replicas.
	// Head-of-line claiming is what makes that true: two relays never hold
	// events of the same user at the same time.
	e := newEnv(t)
	user, err := e.store.CreateUser(t.Context(), "twins@example.com", "V1", true)
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 8; i++ {
		if _, err := e.store.UpdateUser(t.Context(), user.ID, "twins@example.com", fmt.Sprintf("V%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	stopA := e.runRelay(t, amqpURL)
	defer stopA()
	stopB := e.runRelay(t, amqpURL)
	defer stopB()

	msgs := e.consume(t, 8, 20*time.Second)
	var prev int64
	for i, m := range msgs {
		var payload events.Payload
		if err := json.Unmarshal(m.Body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.User.Version <= prev {
			t.Errorf("message %d: version %d after %d — order broken across replicas", i, payload.User.Version, prev)
		}
		prev = payload.User.Version
	}
}

func TestExpiredLeaseIsReclaimed(t *testing.T) {
	e := newEnv(t)
	if _, err := e.store.CreateUser(t.Context(), "leased@example.com", "Leased", true); err != nil {
		t.Fatal(err)
	}
	id := e.eventIDs(t)[0]
	// A replica that died mid-batch: the lease is held and in the future.
	if _, err := e.pool.Exec(t.Context(),
		"UPDATE user_events_outbox SET lease_until = now() + interval '1 hour', leased_by = 'dead-replica' WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}

	stop := e.runRelay(t, amqpURL)
	defer stop()
	time.Sleep(1500 * time.Millisecond)
	if _, published, _, _ := e.outboxRow(t, id); published {
		t.Fatal("a row leased by another replica must not be published")
	}

	// The lease expires: the row goes back to the queue.
	if _, err := e.pool.Exec(t.Context(),
		"UPDATE user_events_outbox SET lease_until = now() - interval '1 second' WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
	e.consume(t, 1, 10*time.Second)
	waitFor(t, 5*time.Second, func() bool { return e.unpublishedCount(t) == 0 })
}

func TestPoisonEventIsQuarantinedAndDoesNotBlockTheUser(t *testing.T) {
	e := newEnvUnbound(t)
	user, err := e.store.CreateUser(t.Context(), "poison@example.com", "V1", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.UpdateUser(t.Context(), user.ID, "poison@example.com", "V2"); err != nil {
		t.Fatal(err)
	}
	ids := e.eventIDs(t)
	poison, next := ids[0], ids[1]

	pub := &fakePublisher{failIDs: map[string]bool{poison: true}}
	stop := e.runRelayWith(t, amqpURL, func(r *events.Relay) {
		r.SetPublisher(pub)
		r.SetRetryPolicy(fastRetry)
	})
	defer stop()

	// The poison row exhausts its budget and is parked; the user's next
	// event, blocked while the poison row was pending, then flows.
	waitFor(t, 10*time.Second, func() bool {
		_, _, quarantined, _ := e.outboxRow(t, poison)
		return quarantined
	})
	attempts, published, _, lastError := e.outboxRow(t, poison)
	if published || attempts != fastRetry.MaxAttempts || lastError == "" {
		t.Errorf("poison row: attempts=%d published=%v last_error=%q", attempts, published, lastError)
	}
	waitFor(t, 10*time.Second, func() bool { return contains(pub.got(), next) })
	if contains(pub.got(), poison) {
		t.Error("the poison row must never have been published")
	}
	waitFor(t, 5*time.Second, func() bool {
		_, quarantined, _, _ := e.metrics.queue()
		return quarantined == 1
	})

	// Recovery: the operator fixes the cause and requeues.
	pub.stopFailing()
	n, err := e.store.RequeueQuarantined(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("RequeueQuarantined = %d, %v; want 1", n, err)
	}
	waitFor(t, 10*time.Second, func() bool {
		_, published, _, _ := e.outboxRow(t, poison)
		return published
	})
	attempts, _, quarantined, _ := e.outboxRow(t, poison)
	if quarantined || attempts != 0 {
		t.Errorf("after requeue: attempts=%d quarantined=%v; want 0/false", attempts, quarantined)
	}
}

func TestUnroutableEventWaitsForAConsumer(t *testing.T) {
	// No queue is bound yet — identity deployed before GSH/DFM. The broker
	// returns the mandatory publish; the event must wait, not vanish and
	// not burn its retry budget.
	e := newEnvUnbound(t)
	if _, err := e.store.CreateUser(t.Context(), "early@example.com", "Early", true); err != nil {
		t.Fatal(err)
	}
	id := e.eventIDs(t)[0]

	stop := e.runRelayWith(t, amqpURL, func(r *events.Relay) {
		r.SetRetryPolicy(events.RetryPolicy{MaxAttempts: 3, BaseBackoff: 10 * time.Millisecond, MaxBackoff: 300 * time.Millisecond})
	})
	defer stop()

	waitFor(t, 5*time.Second, func() bool {
		_, _, _, unroutable := e.metrics.queue()
		return unroutable >= 1
	})
	time.Sleep(700 * time.Millisecond) // a couple more rounds at the max backoff
	attempts, published, quarantined, _ := e.outboxRow(t, id)
	if published || quarantined || attempts != 0 {
		t.Fatalf("unroutable event: published=%v quarantined=%v attempts=%d; want pending with 0 attempts", published, quarantined, attempts)
	}

	// The consumer arrives: the event is delivered on the next round.
	e.bindQueue(t)
	e.consume(t, 1, 10*time.Second)
	waitFor(t, 5*time.Second, func() bool { return e.unpublishedCount(t) == 0 })
}

func TestRetentionTrimsOnlyOldPublishedEvents(t *testing.T) {
	e := newEnvUnbound(t)
	user, err := e.store.CreateUser(t.Context(), "keep@example.com", "Keep", true)
	if err != nil {
		t.Fatal(err)
	}
	// Four rows by hand: old published, recent published, pending, quarantined.
	for _, row := range []struct{ published, quarantined string }{
		{"now() - interval '40 days'", "NULL"},
		{"now() - interval '1 day'", "NULL"},
		{"NULL", "NULL"},
		{"NULL", "now()"},
	} {
		if _, err := e.pool.Exec(t.Context(), fmt.Sprintf(
			`INSERT INTO user_events_outbox (event_type, payload, user_id, published_at, quarantined_at)
			 VALUES ('identity.user.snapshot', '{}'::jsonb, $1, %s, %s)`, row.published, row.quarantined), user.ID); err != nil {
			t.Fatal(err)
		}
	}
	before := len(e.eventIDs(t)) // 4 + the created event

	stop := e.runRelayWith(t, amqpURL, func(r *events.Relay) {
		r.SetPublisher(&fakePublisher{})
		r.SetRetention(30 * 24 * time.Hour)
	})
	time.Sleep(500 * time.Millisecond)
	stop()

	var oldLeft, recentLeft, quarantinedLeft int
	if err := e.pool.QueryRow(t.Context(),
		`SELECT count(*) FILTER (WHERE published_at < now() - interval '30 days'),
		        count(*) FILTER (WHERE published_at >= now() - interval '30 days'),
		        count(*) FILTER (WHERE quarantined_at IS NOT NULL)
		 FROM user_events_outbox`).Scan(&oldLeft, &recentLeft, &quarantinedLeft); err != nil {
		t.Fatal(err)
	}
	if oldLeft != 0 {
		t.Errorf("old published rows left = %d, want 0", oldLeft)
	}
	if quarantinedLeft != 1 {
		t.Errorf("quarantined rows left = %d, want 1 (never trimmed)", quarantinedLeft)
	}
	// Everything the relay published this run counts as recent; the
	// hand-made recent row is among them.
	if recentLeft < 1 || len(e.eventIDs(t)) != before-1 {
		t.Errorf("recent published rows = %d, rows total %d (was %d)", recentLeft, len(e.eventIDs(t)), before)
	}
}

func TestGaugesReflectTheBacklogWhileBrokerIsDown(t *testing.T) {
	e := newEnvUnbound(t)
	for _, email := range []string{"g1@example.com", "g2@example.com"} {
		if _, err := e.store.CreateUser(t.Context(), email, "G", true); err != nil {
			t.Fatal(err)
		}
	}
	stop := e.runRelay(t, "amqp://identity:identity@localhost:1/")
	defer stop()

	waitFor(t, 5*time.Second, func() bool {
		pending, _, oldest, _ := e.metrics.queue()
		return pending == 2 && oldest > 0
	})
	if e.unpublishedCount(t) != 2 {
		t.Error("nothing must be published while the broker is unreachable")
	}
}
