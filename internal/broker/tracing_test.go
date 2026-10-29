package broker

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// memoryBroker — Publisher и Consumer в памяти: опубликованное сообщение
// доставляется как есть, вместе с заголовками.
type memoryBroker struct {
	published []Message
	closed    bool
}

func (b *memoryBroker) Publish(_ context.Context, msg Message) error {
	b.published = append(b.published, msg)
	return nil
}
func (b *memoryBroker) Ping(context.Context) error { return nil }
func (b *memoryBroker) Close() error               { b.closed = true; return nil }

func (b *memoryBroker) Consume(ctx context.Context, h Handler) error {
	for _, msg := range b.published {
		msg.Destination = QueueProcess
		_ = h(ctx, msg)
	}
	return nil
}

func setupTracing(t *testing.T) (*tracetest.SpanRecorder, *sdktrace.TracerProvider) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTextMapPropagator(prev)
		_ = tp.Shutdown(context.Background())
	})
	return rec, tp
}

func spanByKind(t *testing.T, spans []sdktrace.ReadOnlySpan, kind trace.SpanKind) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range spans {
		if s.SpanKind() == kind {
			return s
		}
	}
	t.Fatalf("no %v span among %d", kind, len(spans))
	return nil
}

func TestTracingPropagatesThroughMessage(t *testing.T) {
	rec, tp := setupTracing(t)
	mem := &memoryBroker{}
	pub := TracePublisher(mem, SystemRabbitMQ, ExchangeAvatars, tp)
	con := TraceConsumer(mem, SystemRabbitMQ, tp)

	ctx, request := tp.Tracer("test").Start(context.Background(), "POST /api/v1/avatars")
	callerHeaders := map[string]string{"custom": "v"}
	msg := Message{ID: "m-1", Type: TypeAvatarUploaded, Key: "a-1", Headers: callerHeaders}
	if err := pub.Publish(ctx, msg); err != nil {
		t.Fatal(err)
	}
	request.End()

	if _, ok := callerHeaders["traceparent"]; ok {
		t.Error("Publish modified the caller's headers")
	}
	sent := mem.published[0]
	if sent.Headers["traceparent"] == "" || sent.Headers["custom"] != "v" {
		t.Fatalf("published headers = %v, want traceparent and custom", sent.Headers)
	}

	var handlerSpan trace.SpanContext
	_ = con.Consume(context.Background(), func(ctx context.Context, _ Message) error {
		handlerSpan = trace.SpanContextFromContext(ctx)
		return nil
	})

	producer := spanByKind(t, rec.Ended(), trace.SpanKindProducer)
	consumer := spanByKind(t, rec.Ended(), trace.SpanKindConsumer)

	if producer.Name() != "publish avatars.exchange" || consumer.Name() != "process avatars.process" {
		t.Errorf("span names = %q, %q", producer.Name(), consumer.Name())
	}
	if producer.Parent().SpanID() != request.SpanContext().SpanID() {
		t.Error("producer span is not a child of the request span")
	}
	if consumer.SpanContext().TraceID() != request.SpanContext().TraceID() {
		t.Error("consumer span is in a different trace")
	}
	if consumer.Parent().SpanID() != producer.SpanContext().SpanID() {
		t.Error("consumer span is not a child of the producer span")
	}
	if handlerSpan.SpanID() != consumer.SpanContext().SpanID() {
		t.Error("handler context does not carry the consumer span")
	}

	want := map[string]string{
		"messaging.system":           SystemRabbitMQ,
		"messaging.destination.name": QueueProcess,
		"messaging.message.id":       "m-1",
		"messaging.event_type":       TypeAvatarUploaded,
		"messaging.operation.type":   "process",
	}
	got := map[string]string{}
	for _, kv := range consumer.Attributes() {
		got[string(kv.Key)] = kv.Value.String()
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("consumer %s = %q, want %q", k, got[k], v)
		}
	}

	if err := pub.Ping(context.Background()); err != nil {
		t.Errorf("Ping() = %v", err)
	}
	if err := pub.Close(); err != nil || !mem.closed {
		t.Error("publisher Close is not passed through")
	}
	mem.closed = false
	if err := con.Close(); err != nil || !mem.closed {
		t.Error("consumer Close is not passed through")
	}
}

func TestTracingRecordsHandlerError(t *testing.T) {
	rec, tp := setupTracing(t)
	mem := &memoryBroker{published: []Message{{ID: "m-1", Type: TypeAvatarDeleted}}}
	boom := errors.New("s3 is down")

	_ = TraceConsumer(mem, SystemKafka, tp).Consume(context.Background(), func(context.Context, Message) error {
		return boom
	})

	span := spanByKind(t, rec.Ended(), trace.SpanKindConsumer)
	if span.Status().Code != codes.Error {
		t.Errorf("status = %v, want error", span.Status())
	}
	if span.Parent().IsValid() {
		t.Error("message without traceparent must start a new trace")
	}
}
