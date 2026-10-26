// Package kafka — реализация broker.Publisher и broker.Consumer на Kafka
// (IBM/sarama). Все события идут в топик avatar-events с ключом avatar_id,
// тип и message_id — в заголовках. Топики создаются при старте, если их нет.
package kafka

import (
	"errors"
	"fmt"
	"time"

	"github.com/IBM/sarama"

	"go-avatar-service/internal/broker"
)

// Параметры топиков. Брокер в docker compose один, поэтому фактор репликации 1.
const (
	eventsPartitions  = 3
	dlqPartitions     = 1
	replicationFactor = 1
)

// producerRetries — сколько раз sarama сама повторяет отправку.
// Идемпотентному producer'у нужен хотя бы один повтор.
const producerRetries = 5

// dialTimeout ограничивает подключение к брокеру.
const dialTimeout = 5 * time.Second

var errTopicNotFound = errors.New("kafka topic does not exist")

// newConfig — общая конфигурация: идемпотентный producer с acks=all
// и consumer group с ручным commit.
func newConfig() *sarama.Config {
	cfg := sarama.NewConfig()
	cfg.Version = sarama.V3_5_0_0
	cfg.ClientID = "gophprofile"
	cfg.Net.DialTimeout = dialTimeout

	cfg.Producer.RequiredAcks = sarama.WaitForAll
	cfg.Producer.Idempotent = true
	cfg.Producer.Retry.Max = producerRetries
	cfg.Producer.Return.Successes = true
	// Идемпотентный producer требует не больше одного запроса в полёте.
	cfg.Net.MaxOpenRequests = 1

	cfg.Consumer.Offsets.AutoCommit.Enable = false
	// События, опубликованные до первого старта worker'а, тоже нужно обработать.
	cfg.Consumer.Offsets.Initial = sarama.OffsetOldest
	return cfg
}

// ensureTopics создаёт avatar-events и avatar-events.dlq, если их ещё нет.
func ensureTopics(brokers []string, cfg *sarama.Config) error {
	admin, err := sarama.NewClusterAdmin(brokers, cfg)
	if err != nil {
		return fmt.Errorf("create kafka admin: %w", err)
	}
	defer admin.Close()

	topics := []struct {
		name       string
		partitions int32
	}{
		{broker.TopicEvents, eventsPartitions},
		{broker.TopicDLQ, dlqPartitions},
	}
	for _, t := range topics {
		err := admin.CreateTopic(t.name, &sarama.TopicDetail{
			NumPartitions:     t.partitions,
			ReplicationFactor: replicationFactor,
		}, false)
		if err != nil && !errors.Is(err, sarama.ErrTopicAlreadyExists) {
			return fmt.Errorf("create topic %s: %w", t.name, err)
		}
	}
	return nil
}

// toProducerMessage кладёт событие в топик с ключом avatar_id: все события
// одной аватарки попадают в одну партицию и обрабатываются по порядку.
func toProducerMessage(topic string, msg broker.Message, extra ...sarama.RecordHeader) *sarama.ProducerMessage {
	headers := append([]sarama.RecordHeader{
		{Key: []byte(broker.HeaderEventType), Value: []byte(msg.Type)},
		{Key: []byte(broker.HeaderMessageID), Value: []byte(msg.ID)},
	}, extra...)
	return &sarama.ProducerMessage{
		Topic:   topic,
		Key:     sarama.StringEncoder(msg.Key),
		Value:   sarama.ByteEncoder(msg.Body),
		Headers: headers,
	}
}

func fromConsumerMessage(m *sarama.ConsumerMessage) broker.Message {
	msg := broker.Message{Key: string(m.Key), Body: m.Value}
	for _, h := range m.Headers {
		switch string(h.Key) {
		case broker.HeaderEventType:
			msg.Type = string(h.Value)
		case broker.HeaderMessageID:
			msg.ID = string(h.Value)
		}
	}
	return msg
}
