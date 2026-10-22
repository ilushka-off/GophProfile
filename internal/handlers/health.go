package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"go-avatar-service/internal/httpx"
)

// healthTimeout ограничивает проверку зависимостей в /health,
// чтобы зависшая база не держала запрос пробы.
const healthTimeout = 2 * time.Second

// pinger — зависимость, доступность которой проверяет /health (например, пул БД).
type pinger interface {
	Ping(ctx context.Context) error
}

// HealthHandler обслуживает проверку живости: отвечает о доступности сервиса и базы данных.
type HealthHandler struct {
	db     pinger
	logger *slog.Logger
}

// NewHealthHandler создаёт HealthHandler поверх pinger (обычно пула БД) и логгера.
func NewHealthHandler(db pinger, logger *slog.Logger) *HealthHandler {
	return &HealthHandler{
		db:     db,
		logger: logger,
	}
}

// Check отвечает 200, если зависимости доступны, и 503 в едином формате ошибки — если нет.
func (h *HealthHandler) Check(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
	defer cancel()

	if err := h.db.Ping(ctx); err != nil {
		h.logger.WarnContext(r.Context(), "health check failed", "err", err)
		httpx.WriteError(w, http.StatusServiceUnavailable, msgUnavailable, "database is unavailable")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
