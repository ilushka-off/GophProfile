package kafka

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/testcontainers/testcontainers-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"

	"go-avatar-service/internal/broker"
)

const kafkaImage = "confluentinc/confluent-local:7.5.0"

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startKafka поднимает Kafka в контейнере. Тест пропускается с -short и без Docker.
func startKafka(t *testing.T) []string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test, skipped with -short")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctr, err := tckafka.Run(t.Context(), kafkaImage, tckafka.WithClusterID("gophprofile-test"))
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start kafka: %v", err)
	}
	brokers, err := ctr.Brokers(t.Context())
	if err != nil {
		t.Fatalf("kafka brokers: %v", err)
	}
	return brokers
}

func TestKafka(t *testing.T) {
	brokers := startKafka(t)

	pub, err := NewPublisher(brokers)
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	if err := pub.Ping(t.Context()); err != nil {
		t.Fatalf("ping: %v", err)
	}

	msgs := []broker.Message{
		{ID: "m-ok", Type: broker.TypeAvatarUploaded, Key: "a1", Body: []byte(`{"avatar_id":"a1"}`)},
		{ID: "m-bad", Type: broker.TypeAvatarDeleted, Key: "a2", Body: []byte(`{"avatar_id":"a2"}`)},
	}
	for _, msg := range msgs {
		if err := pub.Publish(t.Context(), msg); err != nil {
			t.Fatalf("publish %s: %v", msg.ID, err)
		}
	}

	consumer, err := NewConsumer(brokers, discardLogger())
	if err != nil {
		t.Fatalf("new consumer: %v", err)
	}
	seen := make(chan broker.Message, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- consumer.Consume(ctx, func(_ context.Context, msg broker.Message) error {
			seen <- msg
			if msg.ID == "m-bad" {
				return errors.New("broken image")
			}
			return nil
		})
	}()

	got := map[string]broker.Message{}
	for range len(msgs) {
		select {
		case msg := <-seen:
			got[msg.ID] = msg
		case <-time.After(30 * time.Second):
			t.Fatalf("got %d messages within 30s, want %d", len(got), len(msgs))
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("consume: %v", err)
	}
	if err := consumer.Close(); err != nil {
		t.Errorf("close consumer: %v", err)
	}

	if m := got["m-ok"]; m.Type != broker.TypeAvatarUploaded || m.Key != "a1" || string(m.Body) != `{"avatar_id":"a1"}` {
		t.Errorf("message = %+v", m)
	}

	dlq := readOne(t, brokers, broker.TopicDLQ)
	var errHeader string
	for _, h := range dlq.Headers {
		if string(h.Key) == broker.HeaderError {
			errHeader = string(h.Value)
		}
	}
	if fromConsumerMessage(dlq).ID != "m-bad" || errHeader != "broken image" {
		t.Errorf("dlq message %+v, error header %q", fromConsumerMessage(dlq), errHeader)
	}

	// Offset закоммичен: новый consumer той же группы старые сообщения не получает.
	again, err := NewConsumer(brokers, discardLogger())
	if err != nil {
		t.Fatalf("new consumer: %v", err)
	}
	defer again.Close()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel2()
	_ = again.Consume(ctx2, func(_ context.Context, msg broker.Message) error {
		t.Errorf("message redelivered after commit: %+v", msg)
		return nil
	})
}

// readOne читает первое сообщение топика с начала.
func readOne(t *testing.T, brokers []string, topic string) *sarama.ConsumerMessage {
	t.Helper()
	c, err := sarama.NewConsumer(brokers, newConfig())
	if err != nil {
		t.Fatalf("new consumer: %v", err)
	}
	defer c.Close()
	pc, err := c.ConsumePartition(topic, 0, sarama.OffsetOldest)
	if err != nil {
		t.Fatalf("consume %s: %v", topic, err)
	}
	defer pc.Close()

	select {
	case m := <-pc.Messages():
		return m
	case <-time.After(10 * time.Second):
		t.Fatalf("no message in %s within 10s", topic)
		return nil
	}
}
