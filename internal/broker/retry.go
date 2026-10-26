package broker

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// DefaultRetryDelays — задержки перед повторами из NFR-3: 1, 2 и 4 секунды.
var DefaultRetryDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// WithRetry повторяет h, пока тот возвращает временную ошибку (ErrRetryable):
// не больше len(delays) раз, перед i-м повтором ждёт delays[i]. Повторы идут
// внутри worker до подтверждения сообщения — так поведение одинаковое
// для RabbitMQ и Kafka. Возвращает последнюю ошибку или ctx.Err(), если ctx
// отменили во время ожидания.
func WithRetry(h Handler, delays []time.Duration, logger *slog.Logger) Handler {
	return func(ctx context.Context, msg Message) error {
		err := h(ctx, msg)
		for attempt := 0; err != nil && errors.Is(err, ErrRetryable) && attempt < len(delays); attempt++ {
			logger.WarnContext(ctx, "retry message",
				"message_id", msg.ID, "type", msg.Type, "key", msg.Key,
				"attempt", attempt+1, "delay", delays[attempt], "err", err)

			timer := time.NewTimer(delays[attempt])
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			err = h(ctx, msg)
		}
		return err
	}
}
