package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"golang.org/x/sync/errgroup"

	"go-avatar-service/internal/broker/factory"
	"go-avatar-service/internal/config"
	"go-avatar-service/internal/handlers"
	"go-avatar-service/internal/middleware"
	"go-avatar-service/internal/observability"
	"go-avatar-service/internal/repository/postgres"
	"go-avatar-service/internal/services"
	"go-avatar-service/internal/storage"
	"go-avatar-service/internal/storage/s3"
	"go-avatar-service/web"
)

// Таймауты HTTP-сервера и корректного завершения.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 10 * time.Second
)

// storageInitTimeout ограничивает проверку и создание бакета при старте,
// чтобы недоступный S3 не подвешивал запуск.
const storageInitTimeout = 10 * time.Second

// version — версия сборки, задаётся через -ldflags "-X main.version=...".
var version = "dev"

// telemetryShutdownTimeout — сколько ждать отправки последних спанов при остановке.
const telemetryShutdownTimeout = 5 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	svc := observability.Service{Name: observability.ServiceServer, Version: version}
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

	if err := postgres.RunMigrations(cfg.DatabaseURI); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	pool, err := postgres.OpenPool(ctx, cfg.DatabaseURI)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	s3Client, err := s3.New(cfg.S3)
	if err != nil {
		return fmt.Errorf("create storage: %w", err)
	}
	initCtx, cancelInit := context.WithTimeout(ctx, storageInitTimeout)
	defer cancelInit()
	if err := s3Client.EnsureBucket(initCtx); err != nil {
		return fmt.Errorf("ensure bucket: %w", err)
	}
	fileStorage := storage.NewTraced(s3Client, cfg.S3.Bucket, otel.GetTracerProvider())

	publisher, err := factory.NewPublisher(cfg.Broker, logger)
	if err != nil {
		return fmt.Errorf("connect to broker: %w", err)
	}
	defer func() {
		if err := publisher.Close(); err != nil {
			slog.Warn("close broker publisher", "err", err)
		}
	}()

	templates, err := web.Templates()
	if err != nil {
		return fmt.Errorf("parse web templates: %w", err)
	}

	metrics := observability.NewRegistry()
	metrics.MustRegister(postgres.NewStorageCollector(pool, logger), postgres.NewPoolCollector(pool))

	avatarService, err := services.NewAvatarService(postgres.NewAvatarRepository(pool), fileStorage, publisher,
		cfg.MaxUploadSize, services.WithMetrics(services.NewMetrics(metrics)))
	if err != nil {
		return fmt.Errorf("create avatar service: %w", err)
	}

	router := handlers.NewRouter(logger,
		handlers.RouterOptions{
			RateLimitRPS:       cfg.RateLimit.RPS,
			RateLimitBurst:     cfg.RateLimit.Burst,
			CORSAllowedOrigins: cfg.CORSAllowedOrigins,
			HTTPMetrics:        middleware.NewHTTPMetrics(metrics),
		},
		handlers.NewHealthHandler(pool, fileStorage, publisher, cfg.Broker.Kind, logger),
		handlers.NewAvatarHandler(avatarService, cfg.MaxUploadSize, logger),
		handlers.NewWebHandler(avatarService, templates, cfg.MaxUploadSize, logger),
	)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	// /metrics — на отдельном адресе, который не публикуется наружу: в метриках
	// есть user_id. Остановка одного сервера останавливает и другой.
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		slog.Info("serving metrics", "addr", cfg.Observability.MetricsAddr)
		return observability.ServeMetrics(ctx, cfg.Observability.MetricsAddr, metrics)
	})
	g.Go(func() error {
		slog.Info("starting server", "addr", cfg.HTTPAddr, "broker", cfg.Broker.Kind)
		return serveHTTP(ctx, srv)
	})
	return g.Wait()
}

// serveHTTP держит srv до отмены ctx и затем корректно его останавливает.
func serveHTTP(ctx context.Context, srv *http.Server) error {
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}

	slog.Info("shutting down server")
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
