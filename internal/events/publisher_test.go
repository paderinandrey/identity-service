package events

import (
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Once the broker closes the AMQP channel, NotifyReturn closes the
// subscriber channel and every receive succeeds at once with a zero
// Return. Both readers must notice the closure and report a transport
// error instead of spinning inside the relay goroutine (Codex review,
// PR #6).
func TestClosedReturnChannelIsATransportError(t *testing.T) {
	newPublisher := func() *amqpPublisher {
		returns := make(chan amqp.Return, 1)
		returns <- amqp.Return{MessageId: "other"} // a stale return before the closure
		close(returns)
		p := newAMQPPublisher("amqp://unused", "x", slog.New(slog.NewTextHandler(io.Discard, nil)))
		p.returns = returns
		return p
	}

	run := func(name string, f func(p *amqpPublisher) error) {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { done <- f(newPublisher()) }()
			select {
			case err := <-done:
				if !errors.Is(err, ErrTransport) {
					t.Errorf("err = %v, want ErrTransport", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("reader spins on the closed return channel")
			}
		})
	}
	run("drainReturns", func(p *amqpPublisher) error { return p.drainReturns() })
	run("returnedID", func(p *amqpPublisher) error {
		_, err := p.returnedID("m1")
		return err
	})
}
