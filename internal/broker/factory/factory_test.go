package factory

import (
	"io"
	"log/slog"
	"testing"

	"go-avatar-service/internal/config"
)

func TestUnsupportedBroker(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.BrokerConfig{Kind: "nats"}

	if _, err := NewPublisher(cfg, logger); err == nil {
		t.Error("NewPublisher: expected error for an unsupported broker")
	}
	if _, err := NewConsumer(cfg, logger); err == nil {
		t.Error("NewConsumer: expected error for an unsupported broker")
	}
}

// Недоступный брокер — ошибка при создании, а не паника или зависание.
func TestUnreachableBroker(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for connection timeouts, skipped with -short")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	for _, cfg := range []config.BrokerConfig{
		{Kind: config.BrokerRabbitMQ, RabbitMQURL: "amqp://guest:guest@127.0.0.1:1/"},
		{Kind: config.BrokerKafka, KafkaBrokers: []string{"127.0.0.1:1"}},
	} {
		t.Run(cfg.Kind, func(t *testing.T) {
			if _, err := NewPublisher(cfg, logger); err == nil {
				t.Error("NewPublisher: expected error")
			}
			if _, err := NewConsumer(cfg, logger); err == nil {
				t.Error("NewConsumer: expected error")
			}
		})
	}
}
