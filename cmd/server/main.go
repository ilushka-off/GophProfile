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

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/handlers"
	"go-avatar-service/internal/repository/postgres"
	"go-avatar-service/internal/storage/s3"
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

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := postgres.RunMigrations(cfg.DatabaseURI); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	pool, err := postgres.OpenPool(ctx, cfg.DatabaseURI)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	storage, err := s3.New(cfg.S3)
	if err != nil {
		return fmt.Errorf("create storage: %w", err)
	}
	initCtx, cancelInit := context.WithTimeout(ctx, storageInitTimeout)
	defer cancelInit()
	if err := storage.EnsureBucket(initCtx); err != nil {
		return fmt.Errorf("ensure bucket: %w", err)
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handlers.NewRouter(logger, handlers.NewHealthHandler(pool, cfg.Broker.Kind, logger)),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("starting server", "addr", cfg.HTTPAddr, "broker", cfg.Broker.Kind)
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
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
