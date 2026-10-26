package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/IBM/sarama"

	"go-avatar-service/internal/broker"
)

var _ broker.Consumer = (*Consumer)(nil)

// Consumer читает avatar-events в consumer group avatars-worker. Offset
// коммитится вручную после обработки или отправки в DLQ. Партиции sarama
// раздаёт по горутинам, поэтому Handler должен быть безопасен для параллельных вызовов.
type Consumer struct {
	logger *slog.Logger
	group  sarama.ConsumerGroup
	dlq    sarama.SyncProducer
}

// NewConsumer подключается к Kafka, создаёт топики, если их нет,
// и готовит producer для DLQ.
func NewConsumer(brokers []string, logger *slog.Logger) (*Consumer, error) {
	cfg := newConfig()
	if err := ensureTopics(brokers, cfg); err != nil {
		return nil, err
	}

	group, err := sarama.NewConsumerGroup(brokers, broker.ConsumerGroup, cfg)
	if err != nil {
		return nil, fmt.Errorf("create kafka consumer group: %w", err)
	}
	dlq, err := sarama.NewSyncProducer(brokers, cfg)
	if err != nil {
		_ = group.Close()
		return nil, fmt.Errorf("create kafka dlq producer: %w", err)
	}
	return &Consumer{logger: logger, group: group, dlq: dlq}, nil
}

// Consume обрабатывает сообщения до отмены ctx. group.Consume возвращается
// при каждой ребалансировке, поэтому вызывается в цикле.
func (c *Consumer) Consume(ctx context.Context, h broker.Handler) error {
	// Ошибку DLQ из ConsumeClaim group.Consume не возвращает — передаём её
	// через причину отмены контекста.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	gh := &groupHandler{consumer: c, handler: h, fail: cancel}
	for {
		err := c.group.Consume(ctx, []string{broker.TopicEvents}, gh)
		if cause := context.Cause(ctx); cause != nil {
			if errors.Is(cause, context.Canceled) {
				return nil
			}
			return cause
		}
		if errors.Is(err, sarama.ErrClosedConsumerGroup) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("consume %s: %w", broker.TopicEvents, err)
		}
	}
}

// Close закрывает consumer group и producer DLQ.
func (c *Consumer) Close() error {
	return errors.Join(c.group.Close(), c.dlq.Close())
}

// groupHandler — sarama.ConsumerGroupHandler поверх broker.Handler.
type groupHandler struct {
	consumer *Consumer
	handler  broker.Handler
	fail     context.CancelCauseFunc
}

func (*groupHandler) Setup(sarama.ConsumerGroupSession) error   { return nil }
func (*groupHandler) Cleanup(sarama.ConsumerGroupSession) error { return nil }

// ConsumeClaim обрабатывает сообщения одной партиции по порядку.
func (g *groupHandler) ConsumeClaim(sess sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	ctx := sess.Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case m, ok := <-claim.Messages():
			if !ok {
				return nil
			}
			if err := g.handle(ctx, sess, m); err != nil {
				g.fail(err)
				return err
			}
		}
	}
}

// handle вызывает обработчик и коммитит offset. Ошибка обработчика —
// сообщение уходит в avatar-events.dlq. Ошибка — только если не удалось
// отправить в DLQ: тогда offset не коммитится и сообщение придёт снова.
func (g *groupHandler) handle(ctx context.Context, sess sarama.ConsumerGroupSession, m *sarama.ConsumerMessage) error {
	msg := fromConsumerMessage(m)

	err := g.handler(ctx, msg)
	if err != nil && ctx.Err() != nil {
		// Остановка или ребалансировка посреди обработки: offset не двигаем.
		return nil
	}
	if err != nil {
		g.consumer.logger.ErrorContext(ctx, "message failed, sending to dlq",
			"message_id", msg.ID, "type", msg.Type, "key", msg.Key, "err", err)
		dlqMsg := toProducerMessage(broker.TopicDLQ, msg,
			sarama.RecordHeader{Key: []byte(broker.HeaderError), Value: []byte(err.Error())})
		if _, _, dlqErr := g.consumer.dlq.SendMessage(dlqMsg); dlqErr != nil {
			return fmt.Errorf("publish to %s: %w", broker.TopicDLQ, dlqErr)
		}
	}

	sess.MarkMessage(m, "")
	sess.Commit()
	return nil
}
