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

func newEnv(t *testing.T) *env {
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
	t.Cleanup(func() { _, _ = channel.QueueDelete(e.queue, false, false, false) })
	t.Cleanup(func() { _ = channel.ExchangeDelete(e.exchange, false, false) })

	queue, err := channel.QueueDeclare("", false, true, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.queue = queue.Name
	if err := channel.QueueBind(e.queue, "user.#", e.exchange, false, nil); err != nil {
		t.Fatal(err)
	}
	return e
}

type fakeRelayMetrics struct {
	mu        sync.Mutex
	published int
	errors    int
	pending   int
}

func (m *fakeRelayMetrics) Published(n int) { m.mu.Lock(); m.published += n; m.mu.Unlock() }
func (m *fakeRelayMetrics) PublishError()   { m.mu.Lock(); m.errors++; m.mu.Unlock() }
func (m *fakeRelayMetrics) PendingSet(n int) {
	m.mu.Lock()
	m.pending = n
	m.mu.Unlock()
}

func (m *fakeRelayMetrics) snapshot() (int, int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.published, m.errors, m.pending
}

func (e *env) runRelay(t *testing.T, url string) (stop func()) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	relay := events.NewRelay(e.pool, url, e.exchange, logger)
	e.metrics = &fakeRelayMetrics{}
	relay.SetMetrics(e.metrics)
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
