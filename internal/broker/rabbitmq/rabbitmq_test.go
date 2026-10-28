package rabbitmq

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/testcontainers/testcontainers-go"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"go-avatar-service/internal/broker"
)

const rabbitImage = "rabbitmq:4.1-alpine"

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startRabbit поднимает RabbitMQ в контейнере и возвращает AMQP URL.
// Тест пропускается с -short и без Docker.
func startRabbit(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test, skipped with -short")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctr, err := tcrabbitmq.Run(t.Context(), rabbitImage)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start rabbitmq: %v", err)
	}
	url, err := ctr.AmqpURL(t.Context())
	if err != nil {
		t.Fatalf("rabbitmq url: %v", err)
	}
	return url
}

// collector — обработчик, который запоминает сообщения и отдаёт их в канал.
type collector struct {
	seen chan broker.Message
	fail func(broker.Message) error
}

func newCollector(fail func(broker.Message) error) *collector {
	return &collector{seen: make(chan broker.Message, 16), fail: fail}
}

func (c *collector) handle(_ context.Context, msg broker.Message) error {
	c.seen <- msg
	if c.fail != nil {
		return c.fail(msg)
	}
	return nil
}

func (c *collector) next(t *testing.T) broker.Message {
	t.Helper()
	select {
	case msg := <-c.seen:
		return msg
	case <-time.After(10 * time.Second):
		t.Fatal("no message delivered within 10s")
		return broker.Message{}
	}
}

// runConsumer запускает Consume в фоне и останавливает его в конце теста.
func runConsumer(t *testing.T, url string, h broker.Handler) {
	t.Helper()
	c, err := NewConsumer(url, discardLogger())
	if err != nil {
		t.Fatalf("new consumer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Consume(ctx, h) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("consume: %v", err)
		}
		if err := c.Close(); err != nil {
			t.Errorf("close consumer: %v", err)
		}
	})
}

func TestRabbitMQ(t *testing.T) {
	url := startRabbit(t)

	pub, err := NewPublisher(url, discardLogger())
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	if err := pub.Ping(t.Context()); err != nil {
		t.Fatalf("ping: %v", err)
	}

	t.Run("routes events to the worker queues", func(t *testing.T) {
		col := newCollector(nil)
		runConsumer(t, url, col.handle)

		for _, msg := range []broker.Message{
			{ID: "m-upload", Type: broker.TypeAvatarUploaded, Key: "a1", Body: []byte(`{"avatar_id":"a1"}`)},
			{ID: "m-delete", Type: broker.TypeAvatarDeleted, Key: "a1", Body: []byte(`{"avatar_id":"a1"}`)},
		} {
			if err := pub.Publish(t.Context(), msg); err != nil {
				t.Fatalf("publish %s: %v", msg.Type, err)
			}
		}

		got := map[string]broker.Message{}
		for range 2 {
			msg := col.next(t)
			got[msg.ID] = msg
		}
		if m := got["m-upload"]; m.Type != broker.TypeAvatarUploaded || m.Key != "a1" || string(m.Body) != `{"avatar_id":"a1"}` {
			t.Errorf("upload message = %+v", m)
		}
		if m := got["m-delete"]; m.Type != broker.TypeAvatarDeleted {
			t.Errorf("delete message = %+v", m)
		}
	})

	t.Run("failed message goes to dlq and is acked", func(t *testing.T) {
		col := newCollector(func(broker.Message) error { return errors.New("broken image") })
		runConsumer(t, url, col.handle)

		msg := broker.Message{ID: "m-bad", Type: broker.TypeAvatarUploaded, Key: "a2", Body: []byte(`{}`)}
		if err := pub.Publish(t.Context(), msg); err != nil {
			t.Fatalf("publish: %v", err)
		}
		col.next(t)

		d := getFromDLQ(t, url)
		if d.MessageId != "m-bad" || d.Headers[broker.HeaderError] != "broken image" {
			t.Errorf("dlq message id %q, error header %v", d.MessageId, d.Headers[broker.HeaderError])
		}
		// Сообщение подтверждено: повторно оно не приходит.
		select {
		case again := <-col.seen:
			t.Errorf("message redelivered: %+v", again)
		case <-time.After(500 * time.Millisecond):
		}
	})

	t.Run("ping fails after close", func(t *testing.T) {
		p, err := NewPublisher(url, discardLogger())
		if err != nil {
			t.Fatalf("new publisher: %v", err)
		}
		if err := p.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		if err := p.Ping(t.Context()); err == nil {
			t.Error("ping after close succeeded")
		}
	})

	t.Run("ping reconnects after lost connection", func(t *testing.T) {
		p, err := NewPublisher(url, discardLogger())
		if err != nil {
			t.Fatalf("new publisher: %v", err)
		}
		t.Cleanup(func() { _ = p.Close() })

		// Так выглядит рестарт брокера для клиента: соединение закрыто.
		_ = p.conn.Close()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := p.Ping(ctx); err != nil {
			t.Fatalf("ping after lost connection: %v", err)
		}
		if err := p.Publish(t.Context(), broker.Message{ID: "m-after", Type: broker.TypeAvatarDeleted, Key: "a3", Body: []byte(`{}`)}); err != nil {
			t.Errorf("publish after reconnect: %v", err)
		}
	})
}

// getFromDLQ ждёт сообщение в avatars.dlq.
func getFromDLQ(t *testing.T, url string) amqp.Delivery {
	t.Helper()
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("channel: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		d, ok, err := ch.Get(broker.QueueDLQ, true)
		if err != nil {
			t.Fatalf("get from dlq: %v", err)
		}
		if ok {
			return d
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("dlq is empty after 10s")
	return amqp.Delivery{}
}
