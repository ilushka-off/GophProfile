package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"go-avatar-service/internal/broker/factory"
	"go-avatar-service/internal/config"
	"go-avatar-service/internal/repository/postgres"
	"go-avatar-service/internal/storage/s3"
	"go-avatar-service/internal/worker"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker stopped", "err", err)
		os.Exit(1)
	}
}

// run подключается к БД, S3 и брокеру и обрабатывает события до SIGTERM.
// Миграции применяет server, поэтому worker стартует после него.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.OpenPool(ctx, cfg.DatabaseURI)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	storage, err := s3.New(cfg.S3)
	if err != nil {
		return fmt.Errorf("create storage: %w", err)
	}

	consumer, err := factory.NewConsumer(cfg.Broker, logger)
	if err != nil {
		return fmt.Errorf("connect to broker: %w", err)
	}
	defer func() {
		if err := consumer.Close(); err != nil {
			slog.Warn("close broker consumer", "err", err)
		}
	}()

	processor := worker.NewProcessor(postgres.NewAvatarRepository(pool), storage, logger)
	handler := processor.MarkFailed(
		worker.Deduplicate(postgres.NewMessageLog(pool), logger, processor.Handle),
	)

	slog.Info("starting worker", "broker", cfg.Broker.Kind)
	if err := consumer.Consume(ctx, handler); err != nil {
		return fmt.Errorf("consume: %w", err)
	}
	slog.Info("shutting down worker")
	return nil
}
