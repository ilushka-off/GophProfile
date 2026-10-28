package worker

import (
	"context"
	"log/slog"

	"go-avatar-service/internal/broker"
)

// MessageLog — журнал обработанных message_id.
type MessageLog interface {
	IsProcessed(ctx context.Context, messageID string) (bool, error)
	MarkProcessed(ctx context.Context, messageID, messageType string) error
}

// Deduplicate пропускает сообщения, чей message_id уже обработан, и отмечает
// успешно обработанные. Это второй рубеж идемпотентности: первый — проверка
// статуса аватарки в самом обработчике, поэтому сбой отметки не страшен
// и только пишется в лог. Сообщения без message_id обрабатываются всегда.
func Deduplicate(log MessageLog, logger *slog.Logger, next broker.Handler) broker.Handler {
	return func(ctx context.Context, msg broker.Message) error {
		if msg.ID == "" {
			return next(ctx, msg)
		}

		done, err := log.IsProcessed(ctx, msg.ID)
		if err != nil {
			return broker.Retryable(err)
		}
		if done {
			logger.InfoContext(ctx, "duplicate message, skip", "message_id", msg.ID, "type", msg.Type, "key", msg.Key)
			return nil
		}

		if err := next(ctx, msg); err != nil {
			return err
		}
		if err := log.MarkProcessed(ctx, msg.ID, msg.Type); err != nil {
			logger.WarnContext(ctx, "mark message processed", "message_id", msg.ID, "err", err)
		}
		return nil
	}
}
