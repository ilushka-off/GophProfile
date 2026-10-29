// Package factory выбирает реализацию брокера по BROKER из конфига.
// Сервисы и worker знают только интерфейсы из internal/broker.
package factory

import (
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/broker/kafka"
	"go-avatar-service/internal/broker/rabbitmq"
	"go-avatar-service/internal/config"
)

// NewPublisher создаёт Publisher для cfg.Kind: rabbitmq или kafka.
// Публикации трейсятся, контекст трейса едет в заголовках сообщений.
func NewPublisher(cfg config.BrokerConfig, logger *slog.Logger) (broker.Publisher, error) {
	switch cfg.Kind {
	case config.BrokerRabbitMQ:
		p, err := rabbitmq.NewPublisher(cfg.RabbitMQURL, logger)
		if err != nil {
			return nil, err
		}
		return broker.TracePublisher(p, broker.SystemRabbitMQ, broker.ExchangeAvatars, otel.GetTracerProvider()), nil
	case config.BrokerKafka:
		p, err := kafka.NewPublisher(cfg.KafkaBrokers)
		if err != nil {
			return nil, err
		}
		return broker.TracePublisher(p, broker.SystemKafka, broker.TopicEvents, otel.GetTracerProvider()), nil
	default:
		return nil, fmt.Errorf("unsupported broker %q", cfg.Kind)
	}
}

// NewConsumer создаёт Consumer для cfg.Kind: rabbitmq или kafka.
// Обработка каждого сообщения продолжает трейс из его заголовков.
func NewConsumer(cfg config.BrokerConfig, logger *slog.Logger) (broker.Consumer, error) {
	switch cfg.Kind {
	case config.BrokerRabbitMQ:
		c, err := rabbitmq.NewConsumer(cfg.RabbitMQURL, logger)
		if err != nil {
			return nil, err
		}
		return broker.TraceConsumer(c, broker.SystemRabbitMQ, otel.GetTracerProvider()), nil
	case config.BrokerKafka:
		c, err := kafka.NewConsumer(cfg.KafkaBrokers, logger)
		if err != nil {
			return nil, err
		}
		return broker.TraceConsumer(c, broker.SystemKafka, otel.GetTracerProvider()), nil
	default:
		return nil, fmt.Errorf("unsupported broker %q", cfg.Kind)
	}
}
