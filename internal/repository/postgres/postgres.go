package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// pingTimeout ограничивает проверку соединения при старте:
// если база не ответила за это время, сервис падает с ошибкой, а не висит.
const (
	pingTimeout = 5 * time.Second
)

// OpenPool создаёт пул соединений к PostgreSQL по databaseURI
// и проверяет, что база отвечает. При ошибке пул закрывается.
func OpenPool(ctx context.Context, databaseURI string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURI)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	pingCtx, pingCancel := context.WithTimeout(ctx, pingTimeout)
	defer pingCancel()

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping pool: %w", err)
	}
	return pool, nil
}
