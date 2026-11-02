package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"go-avatar-service/internal/handlers"
	"go-avatar-service/internal/repository/postgres"
)

// healthcheckTimeout ограничивает запрос подкоманды healthcheck: /health сам
// проверяет зависимости с таймаутом в секунду.
const healthcheckTimeout = 3 * time.Second

// errUsage — неизвестная подкоманда или аргументы.
var errUsage = errors.New("usage: server [migrate up | healthcheck]")

// runCommand выполняет подкоманду из args (os.Args[1:]). Без аргументов
// запускается HTTP-сервер.
//
//	server migrate up — применить миграции и выйти (Helm-хук в Kubernetes);
//	server healthcheck — проверить /health запущенного server (HEALTHCHECK
//	  docker compose: в distroless-образе нет ни shell, ни wget).
func runCommand(args []string) error {
	switch {
	case len(args) == 0:
		return run()
	case len(args) == 2 && args[0] == "migrate" && args[1] == "up":
		return migrateUp()
	case len(args) == 1 && args[0] == "healthcheck":
		return healthcheck(context.Background(), getenv("HTTP_ADDR", ":8080"))
	default:
		return errUsage
	}
}

// migrateUp применяет встроенные миграции к DATABASE_URI. Остальной конфиг
// не нужен: Job миграций получает только Secret с DSN.
func migrateUp() error {
	databaseURI := os.Getenv("DATABASE_URI")
	if databaseURI == "" {
		return errors.New("DATABASE_URI is required")
	}
	slog.Info("applying migrations")
	if err := postgres.RunMigrations(databaseURI); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	slog.Info("migrations applied")
	return nil
}

// healthcheck запрашивает /health у server, слушающего httpAddr на этой же
// машине. Ошибка — если server не ответил 200.
func healthcheck(ctx context.Context, httpAddr string) error {
	url, err := healthURL(httpAddr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, healthcheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("get %s: status %d", url, resp.StatusCode)
	}
	return nil
}

// healthURL строит адрес /health из HTTP_ADDR: ":8080" → http://127.0.0.1:8080/health.
func healthURL(httpAddr string) (string, error) {
	host, port, err := net.SplitHostPort(httpAddr)
	if err != nil {
		return "", fmt.Errorf("HTTP_ADDR %q: %w", httpAddr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + handlers.PathHealth, nil
}

// getenv возвращает переменную окружения или def, если она не задана.
func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}
