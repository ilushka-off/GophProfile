package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"go-avatar-service/internal/httpx"
)

// healthCheckTimeout — таймаут каждой проверки в /health по спеке.
const healthCheckTimeout = time.Second

// Статусы /health по спеке: общий статус сервиса и статус каждого компонента.
const (
	healthOK       = "ok"
	healthDegraded = "degraded"

	componentOK          = "ok"
	componentUnavailable = "unavailable"

	componentDatabase = "database"
)

// pinger — зависимость, доступность которой проверяет /health (например, пул БД).
type pinger interface {
	Ping(ctx context.Context) error
}

// component — проверяемая зависимость и имя, под которым она попадает в ответ.
type component struct {
	name string
	p    pinger
}

// healthResponse — ответ /health:
// {"status":"ok","broker_type":"rabbitmq","components":{"database":"ok"}}.
// Формат ошибок API здесь не используется.
type healthResponse struct {
	Status     string            `json:"status"`
	BrokerType string            `json:"broker_type"`
	Components map[string]string `json:"components"`
}

// HealthHandler обслуживает проверку живости: отвечает о доступности сервиса и его зависимостей.
type HealthHandler struct {
	brokerType string
	components []component
	logger     *slog.Logger
}

// NewHealthHandler создаёт HealthHandler поверх pinger БД. brokerType — значение BROKER
// из конфига (rabbitmq или kafka). Storage и broker добавятся сюда же отдельными компонентами.
func NewHealthHandler(db pinger, brokerType string, logger *slog.Logger) *HealthHandler {
	return &HealthHandler{
		brokerType: brokerType,
		components: []component{{name: componentDatabase, p: db}},
		logger:     logger,
	}
}

// Check проверяет компоненты параллельно, у каждой проверки свой таймаут.
// Отвечает 200 со статусом "ok", если все доступны, и 503 со статусом "degraded", если нет.
func (h *HealthHandler) Check(w http.ResponseWriter, r *http.Request) {
	results := make([]string, len(h.components))

	var wg sync.WaitGroup
	for i, c := range h.components {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(r.Context(), healthCheckTimeout)
			defer cancel()

			if err := c.p.Ping(ctx); err != nil {
				h.logger.WarnContext(r.Context(), "health check failed", "component", c.name, "err", err)
				results[i] = componentUnavailable
				return
			}
			results[i] = componentOK
		})
	}
	wg.Wait()

	resp := healthResponse{
		Status:     healthOK,
		BrokerType: h.brokerType,
		Components: make(map[string]string, len(h.components)),
	}
	for i, c := range h.components {
		resp.Components[c.name] = results[i]
		if results[i] != componentOK {
			resp.Status = healthDegraded
		}
	}

	status := http.StatusOK
	if resp.Status != healthOK {
		status = http.StatusServiceUnavailable
	}
	httpx.WriteJSON(w, status, resp)
}
