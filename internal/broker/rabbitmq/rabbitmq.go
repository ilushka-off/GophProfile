// Package rabbitmq — реализация broker.Publisher и broker.Consumer на RabbitMQ
// (amqp091-go). Топология: topic-exchange avatars.exchange, очереди
// avatars.process (avatar.uploaded, avatar.process), avatars.delete
// (avatar.deleted) и avatars.dlq. Объявляется идемпотентно при старте.
package rabbitmq

import (
	"errors"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"go-avatar-service/internal/broker"
)

// dialTimeout ограничивает установку TCP-соединения, чтобы недоступный брокер
// не подвешивал старт и переподключение.
const dialTimeout = 5 * time.Second

// contentTypeJSON — тело всех событий.
const contentTypeJSON = "application/json"

// bindings — какие routing key попадают в какую очередь.
var bindings = []struct {
	queue string
	key   string
}{
	{broker.QueueProcess, broker.TypeAvatarUploaded},
	{broker.QueueProcess, broker.TypeAvatarProcess},
	{broker.QueueDelete, broker.TypeAvatarDeleted},
}

var errChannelClosed = errors.New("rabbitmq channel is closed")

// dial подключается к брокеру, открывает канал и объявляет топологию.
// timeout ограничивает установку соединения.
func dial(url string, timeout time.Duration) (*amqp.Connection, *amqp.Channel, error) {
	conn, err := amqp.DialConfig(url, amqp.Config{Dial: amqp.DefaultDial(timeout)})
	if err != nil {
		return nil, nil, fmt.Errorf("dial rabbitmq: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("open rabbitmq channel: %w", err)
	}
	if err := declareTopology(ch); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return conn, ch, nil
}

// declareTopology объявляет exchange, очереди и привязки. Повторное объявление
// с теми же параметрами ничего не меняет, поэтому его делают и server, и worker.
func declareTopology(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(broker.ExchangeAvatars, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare exchange %s: %w", broker.ExchangeAvatars, err)
	}
	for _, q := range []string{broker.QueueProcess, broker.QueueDelete, broker.QueueDLQ} {
		if _, err := ch.QueueDeclare(q, true, false, false, false, nil); err != nil {
			return fmt.Errorf("declare queue %s: %w", q, err)
		}
	}
	for _, b := range bindings {
		if err := ch.QueueBind(b.queue, b.key, broker.ExchangeAvatars, false, nil); err != nil {
			return fmt.Errorf("bind queue %s to %s: %w", b.queue, b.key, err)
		}
	}
	return nil
}

// toPublishing переводит сообщение в формат AMQP: тип — в routing key и Type,
// message_id — в MessageId, avatar_id — в заголовок.
func toPublishing(msg broker.Message) amqp.Publishing {
	return amqp.Publishing{
		ContentType:  contentTypeJSON,
		DeliveryMode: amqp.Persistent,
		MessageId:    msg.ID,
		Type:         msg.Type,
		Timestamp:    time.Now().UTC(),
		Headers:      amqp.Table{broker.HeaderAvatarID: msg.Key},
		Body:         msg.Body,
	}
}

// fromDelivery — обратное преобразование для worker. Тип берётся из Type,
// а если его нет (сообщение опубликовано не нами) — из routing key.
func fromDelivery(d amqp.Delivery) broker.Message {
	msgType := d.Type
	if msgType == "" {
		msgType = d.RoutingKey
	}
	key, _ := d.Headers[broker.HeaderAvatarID].(string)
	return broker.Message{ID: d.MessageId, Type: msgType, Key: key, Body: d.Body}
}
