// Package broker — интерфейсы брокера сообщений, с которыми работают сервисы и worker.
// Реализации RabbitMQ и Kafka и выбор между ними по BROKER — отдельной задачей.
package broker

import "context"

// Message — сообщение брокера независимо от реализации.
type Message struct {
	ID   string // message_id, UUID
	Type string // avatar.uploaded | avatar.process | avatar.deleted
	Key  string // avatar_id
	Body []byte // JSON события
}

// Publisher публикует сообщения в брокер.
type Publisher interface {
	Publish(ctx context.Context, msg Message) error
	Ping(ctx context.Context) error
	Close() error
}
