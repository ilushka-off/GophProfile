package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"golang.org/x/sync/errgroup"

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/broker/factory"
	"go-avatar-service/internal/config"
	"go-avatar-service/internal/observability"
	"go-avatar-service/internal/repository/postgres"
	"go-avatar-service/internal/storage"
	"go-avatar-service/internal/storage/s3"
	"go-avatar-service/internal/worker"
)

// version — версия сборки, задаётся через -ldflags "-X main.version=...".
var version = "dev"

// telemetryShutdownTimeout — сколько ждать отправки последних спанов при остановке.
const telemetryShutdownTimeout = 5 * time.Second

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

	svc := observability.Service{Name: observability.ServiceWorker, Version: version}
	logger := observability.NewLogger(os.Stdout, cfg.LogLevel, svc)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTelemetry, err := observability.Setup(ctx, cfg.Observability, svc)
	if err != nil {
		return fmt.Errorf("setup telemetry: %w", err)
	}
	defer func() {
		// ctx уже отменён сигналом, а спаны нужно успеть отправить.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), telemetryShutdownTimeout)
		defer cancel()
		if err := shutdownTelemetry(shutdownCtx); err != nil {
			slog.Warn("shutdown telemetry", "err", err)
		}
	}()

	pool, err := postgres.OpenPool(ctx, cfg.DatabaseURI)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	s3Client, err := s3.New(cfg.S3)
	if err != nil {
		return fmt.Errorf("create storage: %w", err)
	}
	fileStorage := storage.NewTraced(s3Client, cfg.S3.Bucket, otel.GetTracerProvider())

	consumer, err := factory.NewConsumer(cfg.Broker, logger)
	if err != nil {
		return fmt.Errorf("connect to broker: %w", err)
	}
	defer func() {
		if err := consumer.Close(); err != nil {
			slog.Warn("close broker consumer", "err", err)
		}
	}()

	processor := worker.NewProcessor(postgres.NewAvatarRepository(pool), fileStorage, logger)
	metrics := observability.NewRegistry()
	metrics.MustRegister(postgres.NewPoolCollector(pool))

	// Порядок обёрток: временные ошибки повторяются (1, 2, 4 с) вместе с проверкой
	// message_id; если повторы кончились или ошибка постоянная, аватарка
	// получает статус failed, а consumer отправляет сообщение в DLQ. Метрики
	// снаружи всего: считают итоговый исход события.
	handler := worker.NewMetrics(metrics).Instrument(processor.MarkFailed(
		broker.WithRetry(
			worker.Deduplicate(postgres.NewMessageLog(pool), logger, processor.Handle),
			broker.DefaultRetryDelays, logger,
		),
	))

	// Сервер метрик живёт, пока идёт обработка: остановка consumer'а
	// (сигнал или потеря брокера) останавливает и его, и наоборот.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		slog.Info("serving metrics", "addr", cfg.Observability.MetricsAddr)
		return observability.ServeMetrics(ctx, cfg.Observability.MetricsAddr, metrics)
	})
	g.Go(func() error {
		defer cancel()
		slog.Info("starting worker", "broker", cfg.Broker.Kind)
		if err := consumer.Consume(ctx, handler); err != nil {
			return fmt.Errorf("consume: %w", err)
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		return err
	}
	slog.Info("shutting down worker")
	return nil
}
