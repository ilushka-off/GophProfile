package kafka

import (
	"testing"
	"time"

	"github.com/IBM/sarama"

	"go-avatar-service/internal/broker"
)

func TestHeadersRoundTrip(t *testing.T) {
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	msg := broker.Message{
		ID: "m-1", Type: broker.TypeAvatarUploaded, Key: "a-1", Body: []byte("{}"),
		Headers: map[string]string{"traceparent": traceparent},
	}

	ts := time.Date(2026, 1, 2, 3, 4, 5, 6000000, time.UTC)
	pm := toProducerMessage(broker.TopicEvents, msg)
	headers := make([]*sarama.RecordHeader, len(pm.Headers))
	for i := range pm.Headers {
		headers[i] = &pm.Headers[i]
	}
	got := fromConsumerMessage(&sarama.ConsumerMessage{
		Topic: pm.Topic, Key: []byte(msg.Key), Value: msg.Body, Headers: headers, Timestamp: ts,
	})

	if got.ID != msg.ID || got.Type != msg.Type || got.Key != msg.Key || got.Destination != broker.TopicEvents {
		t.Errorf("fromConsumerMessage() = %+v", got)
	}
	if got.Headers["traceparent"] != traceparent {
		t.Errorf("traceparent = %q, want %q", got.Headers["traceparent"], traceparent)
	}
	if !got.PublishedAt.Equal(ts) {
		t.Errorf("PublishedAt = %v, want record timestamp %v", got.PublishedAt, ts)
	}
	if _, ok := got.Headers[broker.HeaderMessageID]; ok {
		t.Error("message_id is duplicated into Headers")
	}
}
