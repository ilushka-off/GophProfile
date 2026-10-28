// Package factory выбирает реализацию брокера по BROKER из конфига.
// Сервисы и worker знают только интерфейсы из internal/broker.
package factory

import (
	"fmt"
	"log/slog"

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/broker/kafka"
	"go-avatar-service/internal/broker/rabbitmq"
	"go-avatar-service/internal/config"
)

// NewPublisher создаёт Publisher для cfg.Kind: rabbitmq или kafka.
func NewPublisher(cfg config.BrokerConfig, logger *slog.Logger) (broker.Publisher, error) {
	switch cfg.Kind {
	case config.BrokerRabbitMQ:
		return rabbitmq.NewPublisher(cfg.RabbitMQURL, logger)
	case config.BrokerKafka:
		return kafka.NewPublisher(cfg.KafkaBrokers)
	default:
		return nil, fmt.Errorf("unsupported broker %q", cfg.Kind)
	}
}

// NewConsumer создаёт Consumer для cfg.Kind: rabbitmq или kafka.
func NewConsumer(cfg config.BrokerConfig, logger *slog.Logger) (broker.Consumer, error) {
	switch cfg.Kind {
	case config.BrokerRabbitMQ:
		return rabbitmq.NewConsumer(cfg.RabbitMQURL, logger)
	case config.BrokerKafka:
		return kafka.NewConsumer(cfg.KafkaBrokers, logger)
	default:
		return nil, fmt.Errorf("unsupported broker %q", cfg.Kind)
	}
}
