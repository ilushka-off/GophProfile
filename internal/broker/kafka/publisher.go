package kafka

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/IBM/sarama"

	"go-avatar-service/internal/broker"
)

var _ broker.Publisher = (*Publisher)(nil)

// Publisher публикует события в avatar-events синхронно: Publish возвращается,
// когда все реплики подтвердили запись (acks=all).
type Publisher struct {
	client   sarama.Client
	producer sarama.SyncProducer
}

// NewPublisher подключается к Kafka и создаёт топики, если их нет.
func NewPublisher(brokers []string) (*Publisher, error) {
	cfg := newConfig()
	if err := ensureTopics(brokers, cfg); err != nil {
		return nil, err
	}

	client, err := sarama.NewClient(brokers, cfg)
	if err != nil {
		return nil, fmt.Errorf("create kafka client: %w", err)
	}
	producer, err := sarama.NewSyncProducerFromClient(client)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("create kafka producer: %w", err)
	}
	return &Publisher{client: client, producer: producer}, nil
}

// Publish отправляет событие в avatar-events. sarama не принимает контекст,
// поэтому отмена ctx отправку не прерывает.
func (p *Publisher) Publish(_ context.Context, msg broker.Message) error {
	if _, _, err := p.producer.SendMessage(toProducerMessage(broker.TopicEvents, msg)); err != nil {
		return fmt.Errorf("publish to kafka: %w", err)
	}
	return nil
}

// Ping проверяет, что хотя бы один брокер отвечает и топик avatar-events есть.
// Запрос метаданных идёт в горутине, чтобы уложиться в таймаут ctx.
func (p *Publisher) Ping(ctx context.Context) error {
	done := make(chan error, 1)
	go func() {
		if err := p.client.RefreshMetadata(); err != nil {
			done <- err
			return
		}
		topics, err := p.client.Topics()
		if err != nil {
			done <- err
			return
		}
		if !slices.Contains(topics, broker.TopicEvents) {
			done <- errTopicNotFound
			return
		}
		done <- nil
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// Close закрывает producer и клиент.
func (p *Publisher) Close() error {
	return errors.Join(p.producer.Close(), p.client.Close())
}
