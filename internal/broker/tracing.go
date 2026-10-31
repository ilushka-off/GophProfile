package broker

import (
	"context"
	"maps"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// tracerName — имя инструментирования в спанах брокера.
const tracerName = "go-avatar-service/internal/broker"

// Значения messaging.system.
const (
	SystemRabbitMQ = "rabbitmq"
	SystemKafka    = "kafka"
)

// attrEventType — тип события (avatar.uploaded, ...): в имени спана
// стоит очередь или топик, а тип нужен, чтобы отличать события.
const attrEventType = attribute.Key("messaging.event_type")

// TracePublisher оборачивает Publisher: каждое сообщение публикуется в спане
// kind=Producer, а контекст трейса (traceparent) уходит в заголовках
// сообщения, чтобы worker продолжил тот же трейс. destination — exchange
// или топик, куда публикует next.
func TracePublisher(next Publisher, system, destination string, tp trace.TracerProvider) Publisher {
	return &tracedPublisher{next: next, system: system, destination: destination, tracer: tp.Tracer(tracerName)}
}

type tracedPublisher struct {
	next        Publisher
	system      string
	destination string
	tracer      trace.Tracer
}

func (p *tracedPublisher) Publish(ctx context.Context, msg Message) error {
	ctx, span := p.tracer.Start(ctx, "publish "+p.destination,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(messageAttrs(p.system, p.destination, msg)...),
	)
	defer span.End()
	span.SetAttributes(semconv.MessagingOperationTypeSend)

	// Копия: заголовки вызывающего не меняем.
	headers := make(map[string]string, len(msg.Headers)+2)
	maps.Copy(headers, msg.Headers)
	otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(headers))
	msg.Headers = headers

	err := p.next.Publish(ctx, msg)
	recordError(span, err)
	return err
}

func (p *tracedPublisher) Ping(ctx context.Context) error { return p.next.Ping(ctx) }
func (p *tracedPublisher) Close() error                   { return p.next.Close() }

// TraceConsumer оборачивает Consumer: обработка каждого сообщения идёт в спане
// kind=Consumer, дочернем к спану публикации из заголовков сообщения. Спаны
// обработчика (БД, S3, миниатюры) и его логи попадают в тот же трейс.
func TraceConsumer(next Consumer, system string, tp trace.TracerProvider) Consumer {
	return &tracedConsumer{next: next, system: system, tracer: tp.Tracer(tracerName)}
}

type tracedConsumer struct {
	next   Consumer
	system string
	tracer trace.Tracer
}

func (c *tracedConsumer) Consume(ctx context.Context, h Handler) error {
	return c.next.Consume(ctx, func(ctx context.Context, msg Message) error {
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(msg.Headers))
		ctx, span := c.tracer.Start(ctx, "process "+msg.Destination,
			trace.WithSpanKind(trace.SpanKindConsumer),
			trace.WithAttributes(messageAttrs(c.system, msg.Destination, msg)...),
		)
		defer span.End()
		span.SetAttributes(semconv.MessagingOperationTypeProcess)

		err := h(ctx, msg)
		recordError(span, err)
		return err
	})
}

func (c *tracedConsumer) Close() error { return c.next.Close() }

func messageAttrs(system, destination string, msg Message) []attribute.KeyValue {
	return []attribute.KeyValue{
		semconv.MessagingSystemKey.String(system),
		semconv.MessagingDestinationName(destination),
		semconv.MessagingMessageID(msg.ID),
		attrEventType.String(msg.Type),
	}
}

func recordError(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
