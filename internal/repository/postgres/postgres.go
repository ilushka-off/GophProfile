package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pingTimeout ограничивает проверку соединения при старте:
// если база не ответила за это время, сервис падает с ошибкой, а не висит.
const (
	pingTimeout = 5 * time.Second
)

// OpenPool создаёт пул соединений к PostgreSQL по databaseURI
// и проверяет, что база отвечает. При ошибке пул закрывается.
// Каждый запрос внутри трейса пишется дочерним спаном (db.system.name =
// postgresql, текст запроса без параметров). Запросы вне трейса, например
// проверки /health, спанов не создают.
func OpenPool(ctx context.Context, databaseURI string) (*pgxpool.Pool, error) {
	// Спаны ожидания свободного соединения только загромождают трейс:
	// время ожидания видно в метриках пула.
	return openPool(ctx, databaseURI, otelpgx.NewTracer(otelpgx.WithDisableAcquireTracer()))
}

func openPool(ctx context.Context, databaseURI string, tracer pgx.QueryTracer) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURI)
	if err != nil {
		return nil, fmt.Errorf("parse database uri: %w", err)
	}
	cfg.ConnConfig.Tracer = tracer

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
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
