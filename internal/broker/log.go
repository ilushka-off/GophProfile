package broker

import (
	"context"
	"log/slog"
)

// LogPublisher только пишет сообщение в лог. Временная заглушка для server,
// пока нет реализаций RabbitMQ и Kafka: их подключит фабрика по BROKER.
type LogPublisher struct {
	logger *slog.Logger
}

// NewLogPublisher создаёт LogPublisher.
func NewLogPublisher(logger *slog.Logger) *LogPublisher {
	return &LogPublisher{logger: logger}
}

// Publish пишет сообщение в лог и всегда завершается успешно.
func (p *LogPublisher) Publish(ctx context.Context, msg Message) error {
	p.logger.InfoContext(ctx, "publish message (log publisher)",
		"message_id", msg.ID, "type", msg.Type, "key", msg.Key, "body", string(msg.Body))
	return nil
}

// Ping всегда успешен.
func (p *LogPublisher) Ping(context.Context) error { return nil }

// Close ничего не делает.
func (p *LogPublisher) Close() error { return nil }
