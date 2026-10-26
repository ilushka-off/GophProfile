package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	amqp "github.com/rabbitmq/amqp091-go"
	"golang.org/x/sync/errgroup"

	"go-avatar-service/internal/broker"
)

var _ broker.Consumer = (*Consumer)(nil)

// prefetch — сколько неподтверждённых сообщений брокер отдаёт worker'у заранее.
const prefetch = 10

// Consumer читает очереди avatars.process и avatars.delete, каждую в своей
// горутине. Сообщения подтверждаются вручную.
type Consumer struct {
	logger *slog.Logger
	conn   *amqp.Connection
	ch     *amqp.Channel
}

// NewConsumer подключается к RabbitMQ, объявляет топологию и ставит prefetch.
func NewConsumer(url string, logger *slog.Logger) (*Consumer, error) {
	conn, ch, err := dial(url)
	if err != nil {
		return nil, err
	}
	if err := ch.Qos(prefetch, 0, false); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("set prefetch: %w", err)
	}
	return &Consumer{logger: logger, conn: conn, ch: ch}, nil
}

// Consume обрабатывает обе очереди до отмены ctx. Если брокер закрыл канал,
// возвращает ошибку: worker завершится, а неподтверждённые сообщения
// вернутся в очередь.
func (c *Consumer) Consume(ctx context.Context, h broker.Handler) error {
	g, ctx := errgroup.WithContext(ctx)
	for _, queue := range []string{broker.QueueProcess, broker.QueueDelete} {
		deliveries, err := c.ch.ConsumeWithContext(ctx, queue, "", false, false, false, false, nil)
		if err != nil {
			return fmt.Errorf("consume %s: %w", queue, err)
		}
		g.Go(func() error {
			return c.consumeQueue(ctx, queue, deliveries, h)
		})
	}
	return g.Wait()
}

func (c *Consumer) consumeQueue(ctx context.Context, queue string, deliveries <-chan amqp.Delivery, h broker.Handler) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				// Канал закрылся сам, а не из-за остановки — соединение с брокером потеряно.
				if ctx.Err() == nil {
					return fmt.Errorf("consume %s: %w", queue, errChannelClosed)
				}
				return nil
			}
			if err := c.handle(ctx, d, h); err != nil {
				return err
			}
		}
	}
}

// handle вызывает обработчик и подтверждает сообщение. Ошибка обработчика —
// сообщение уходит в avatars.dlq и подтверждается. Ошибка — только если не
// удалось подтвердить или отправить в DLQ.
func (c *Consumer) handle(ctx context.Context, d amqp.Delivery, h broker.Handler) error {
	msg := fromDelivery(d)

	err := h(ctx, msg)
	if err != nil && ctx.Err() != nil {
		// Остановка посреди обработки: не подтверждаем, брокер доставит заново.
		return nil //nolint:nilerr // ошибка вызвана остановкой, это не сбой consumer'а
	}
	if err != nil {
		c.logger.ErrorContext(ctx, "message failed, sending to dlq",
			"message_id", msg.ID, "type", msg.Type, "key", msg.Key, "err", err)
		if dlqErr := c.toDLQ(ctx, msg, err); dlqErr != nil {
			_ = d.Nack(false, true)
			return dlqErr
		}
	}

	if err := d.Ack(false); err != nil {
		return fmt.Errorf("ack message %s: %w", msg.ID, err)
	}
	return nil
}

// toDLQ кладёт сообщение в avatars.dlq через default exchange с причиной в заголовке.
func (c *Consumer) toDLQ(ctx context.Context, msg broker.Message, cause error) error {
	p := toPublishing(msg)
	p.Headers[broker.HeaderError] = cause.Error()
	// Сообщение уже прочитано: в DLQ его нужно положить, даже если ctx отменяют.
	err := c.ch.PublishWithContext(context.WithoutCancel(ctx), "", broker.QueueDLQ, false, false, p)
	if err != nil {
		return fmt.Errorf("publish to %s: %w", broker.QueueDLQ, err)
	}
	return nil
}

// Close закрывает канал и соединение.
func (c *Consumer) Close() error {
	err := c.conn.Close()
	if errors.Is(err, amqp.ErrClosed) {
		return nil
	}
	return err
}
