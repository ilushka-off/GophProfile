package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MessageLog хранит message_id обработанных worker'ом сообщений.
type MessageLog struct {
	pool *pgxpool.Pool
}

// NewMessageLog создаёт MessageLog поверх пула соединений.
func NewMessageLog(pool *pgxpool.Pool) *MessageLog {
	return &MessageLog{pool: pool}
}

// IsProcessed сообщает, обработано ли уже сообщение с этим message_id.
func (l *MessageLog) IsProcessed(ctx context.Context, messageID string) (bool, error) {
	var exists bool
	err := l.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM processed_messages WHERE message_id = $1)`, messageID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check processed message: %w", err)
	}
	return exists, nil
}

// MarkProcessed запоминает сообщение как обработанное. Повторная отметка — не ошибка.
func (l *MessageLog) MarkProcessed(ctx context.Context, messageID, messageType string) error {
	_, err := l.pool.Exec(ctx, `
		INSERT INTO processed_messages (message_id, message_type)
		VALUES ($1, $2)
		ON CONFLICT (message_id) DO NOTHING`, messageID, messageType)
	if err != nil {
		return fmt.Errorf("mark message processed: %w", err)
	}
	return nil
}
