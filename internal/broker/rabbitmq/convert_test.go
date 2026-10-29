package rabbitmq

import (
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"

	"go-avatar-service/internal/broker"
)

func TestHeadersRoundTrip(t *testing.T) {
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	msg := broker.Message{
		ID: "m-1", Type: broker.TypeAvatarUploaded, Key: "a-1", Body: []byte("{}"),
		Headers: map[string]string{"traceparent": traceparent},
	}

	p := toPublishing(msg)
	got := fromDelivery(broker.QueueProcess, amqp.Delivery{
		Headers: p.Headers, MessageId: p.MessageId, Type: p.Type, Body: p.Body,
	})

	if got.ID != msg.ID || got.Type != msg.Type || got.Key != msg.Key || got.Destination != broker.QueueProcess {
		t.Errorf("fromDelivery() = %+v", got)
	}
	if got.Headers["traceparent"] != traceparent {
		t.Errorf("traceparent = %q, want %q", got.Headers["traceparent"], traceparent)
	}
	if _, ok := got.Headers[broker.HeaderAvatarID]; ok {
		t.Error("avatar_id is duplicated into Headers")
	}
}

func TestFromDeliveryIgnoresNonStringHeaders(t *testing.T) {
	got := fromDelivery(broker.QueueDelete, amqp.Delivery{
		RoutingKey: broker.TypeAvatarDeleted,
		Headers:    amqp.Table{"x-death": []any{}, "x-count": int32(1)},
	})
	if got.Headers != nil {
		t.Errorf("Headers = %v, want nil", got.Headers)
	}
	if got.Type != broker.TypeAvatarDeleted {
		t.Errorf("Type = %q, want routing key", got.Type)
	}
}
