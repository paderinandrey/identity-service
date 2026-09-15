package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// ErrUnroutable reports that the broker accepted the message but no queue
// was bound for its routing key. It is a topology condition, not a
// failure: the event waits for a consumer instead of burning attempts.
var ErrUnroutable = errors.New("no queue bound for the event")

// Message is one outbox event as handed to a Publisher.
type Message struct {
	ID         string
	Type       string
	RoutingKey string
	Body       []byte
}

// Publisher delivers one message with broker confirmation. The relay owns
// the interface so tests can substitute a failing publisher against the
// real database; AMQP is the production implementation.
type Publisher interface {
	Publish(ctx context.Context, m Message) error
	Close()
}

// amqpPublisher publishes to a durable topic exchange with publisher
// confirms and mandatory routing. The exchange is the only topology this
// service owns; queues and bindings belong to the consumers.
type amqpPublisher struct {
	url      string
	exchange string
	logger   *slog.Logger

	conn    *amqp.Connection
	channel *amqp.Channel
	returns chan amqp.Return
}

func newAMQPPublisher(url, exchange string, logger *slog.Logger) *amqpPublisher {
	return &amqpPublisher{url: url, exchange: exchange, logger: logger}
}

func (p *amqpPublisher) ensureChannel() error {
	if p.channel != nil && !p.channel.IsClosed() {
		return nil
	}
	p.Close()
	conn, err := amqp.Dial(p.url)
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
	if err := channel.ExchangeDeclare(p.exchange, "topic", true, false, false, false, nil); err != nil {
		_ = conn.Close()
		return fmt.Errorf("declare exchange: %w", err)
	}
	// Mandatory publishes that no queue accepts come back here; the
	// broker sends basic.return before the confirm ack, and publishes are
	// sequential, so the return for a message is queued by the time its
	// confirmation resolves.
	p.returns = channel.NotifyReturn(make(chan amqp.Return, 64))
	p.conn, p.channel = conn, channel
	p.logger.Info("connected to RabbitMQ", "exchange", p.exchange)
	return nil
}

// Publish sends one message and waits for the broker's confirmation.
func (p *amqpPublisher) Publish(ctx context.Context, m Message) error {
	if err := p.ensureChannel(); err != nil {
		return err
	}
	p.drainReturns()
	confirmation, err := p.channel.PublishWithDeferredConfirmWithContext(ctx,
		p.exchange, m.RoutingKey,
		true,  // mandatory: an unroutable message must come back, not vanish
		false, // immediate: unsupported by RabbitMQ
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			MessageId:    m.ID,
			Type:         m.Type,
			Timestamp:    time.Now(),
			Body:         m.Body,
		})
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	ok, err := confirmation.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("confirm: %w", err)
	}
	if !ok {
		return fmt.Errorf("broker nacked message %s", m.ID)
	}
	if p.returnedID(m.ID) {
		return ErrUnroutable
	}
	return nil
}

// drainReturns discards returns left over from earlier messages.
func (p *amqpPublisher) drainReturns() {
	for {
		select {
		case <-p.returns:
		default:
			return
		}
	}
}

// returnedID reports whether the broker returned the given message.
func (p *amqpPublisher) returnedID(id string) bool {
	for {
		select {
		case ret := <-p.returns:
			if ret.MessageId == id {
				return true
			}
		default:
			return false
		}
	}
}

// Close drops the connection; the next Publish reconnects.
func (p *amqpPublisher) Close() {
	if p.channel != nil {
		_ = p.channel.Close()
		p.channel = nil
	}
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
	}
}
