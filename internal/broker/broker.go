// Package broker — интерфейсы брокера сообщений, с которыми работают сервисы и worker.
// Реализации: internal/broker/rabbitmq и internal/broker/kafka, выбор между
// ними по BROKER — internal/broker/factory.
package broker

import (
	"context"
	"errors"
	"time"
)

// Message — сообщение брокера независимо от реализации.
type Message struct {
	ID   string // message_id, UUID
	Type string // avatar.uploaded | avatar.process | avatar.deleted
	Key  string // avatar_id
	Body []byte // JSON события

	// Headers — дополнительные заголовки сообщения, например traceparent
	// для сквозного трейса. Едут в AMQP headers или Kafka record headers.
	Headers map[string]string
	// Destination — очередь (RabbitMQ) или топик (Kafka), из которого
	// сообщение прочитано. Заполняет Consumer.
	Destination string
	// PublishedAt — когда сообщение опубликовано, по часам publisher'а.
	// Заполняет Consumer; нулевое, если брокер время не передал.
	PublishedAt time.Time
}

// Publisher публикует сообщения в брокер.
type Publisher interface {
	Publish(ctx context.Context, msg Message) error
	Ping(ctx context.Context) error
	Close() error
}

// Handler обрабатывает одно сообщение. nil — сообщение обработано;
// ошибка, обёрнутая Retryable, — временная, сообщение стоит повторить;
// любая другая ошибка — сообщение сразу уходит в DLQ.
type Handler func(ctx context.Context, msg Message) error

// Consumer доставляет сообщения в Handler. Сообщение подтверждается (ack или
// commit offset) после того, как Handler вернул nil или сообщение ушло в DLQ.
// Если ctx отменён посреди обработки, сообщение не подтверждается и будет
// доставлено повторно.
type Consumer interface {
	// Consume блокирует до отмены ctx или до ошибки соединения с брокером.
	Consume(ctx context.Context, h Handler) error
	Close() error
}

// Имена топологии из спеки (раздел 5 «API v1 — описание методов»).
const (
	// RabbitMQ.
	ExchangeAvatars = "avatars.exchange"
	QueueProcess    = "avatars.process"
	QueueDelete     = "avatars.delete"
	QueueDLQ        = "avatars.dlq"

	// Kafka.
	TopicEvents   = "avatar-events"
	TopicDLQ      = "avatar-events.dlq"
	ConsumerGroup = "avatars-worker"
)

// Заголовки сообщения. В Kafka в заголовках едут тип и message_id, в обоих
// брокерах — причина отправки в DLQ.
const (
	HeaderEventType = "event_type"
	HeaderMessageID = "message_id"
	HeaderAvatarID  = "avatar_id"
	HeaderError     = "x-error"
)

// ErrRetryable помечает временную ошибку: её можно повторить с backoff.
// Проверяется через errors.Is.
var ErrRetryable = errors.New("retryable")

// Retryable помечает err как временную. nil остаётся nil.
func Retryable(err error) error {
	if err == nil {
		return nil
	}
	return retryableError{err: err}
}

type retryableError struct{ err error }

func (e retryableError) Error() string   { return e.err.Error() }
func (e retryableError) Unwrap() []error { return []error{e.err, ErrRetryable} }
