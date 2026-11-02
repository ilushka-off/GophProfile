package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"go-avatar-service/internal/httpx"
)

// healthCheckTimeout — таймаут каждой проверки в /health по спеке.
const healthCheckTimeout = time.Second

// Статусы /health по спеке: общий статус сервиса и статус каждого компонента.
const (
	healthOK       = "ok"
	healthDegraded = "degraded"
	// healthShuttingDown — получен SIGTERM: под доживает последние секунды,
	// новые запросы на него слать не нужно.
	healthShuttingDown = "shutting_down"

	componentOK          = "ok"
	componentUnavailable = "unavailable"

	componentDatabase = "database"
	componentStorage  = "storage"
	componentBroker   = "broker"
)

// errBucketNotFound — S3 отвечает, но бакета нет: загружать некуда.
var errBucketNotFound = errors.New("bucket does not exist")

// pinger — зависимость, доступность которой проверяет /health (например, пул БД).
type pinger interface {
	Ping(ctx context.Context) error
}

// bucketChecker — хранилище, которое умеет проверить свой бакет (клиент S3).
type bucketChecker interface {
	BucketExists(ctx context.Context) (bool, error)
}

// storagePinger проверяет S3 через BucketExists: доступен и бакет на месте.
type storagePinger struct {
	storage bucketChecker
}

func (p storagePinger) Ping(ctx context.Context) error {
	exists, err := p.storage.BucketExists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return errBucketNotFound
	}
	return nil
}

// component — проверяемая зависимость и имя, под которым она попадает в ответ.
type component struct {
	name string
	p    pinger
}

// healthResponse — ответ /health:
// {"status":"ok","broker_type":"rabbitmq","components":{"broker":"ok","database":"ok","storage":"ok"}}.
// Формат ошибок API здесь не используется.
type healthResponse struct {
	Status     string            `json:"status"`
	BrokerType string            `json:"broker_type"`
	Components map[string]string `json:"components"`
}

// HealthHandler обслуживает проверку готовности: отвечает о доступности сервиса
// и его зависимостей. В Kubernetes это readinessProbe.
type HealthHandler struct {
	brokerType string
	components []component
	logger     *slog.Logger
	draining   atomic.Bool
}

// NewHealthHandler создаёт HealthHandler, который проверяет БД, S3 и брокер.
// brokerType — значение BROKER из конфига (rabbitmq или kafka). broker == nil —
// брокер не проверяется: у worker'а потеря соединения с брокером и так
// завершает процесс.
func NewHealthHandler(db pinger, storage bucketChecker, broker pinger, brokerType string, logger *slog.Logger) *HealthHandler {
	components := []component{
		{name: componentDatabase, p: db},
		{name: componentStorage, p: storagePinger{storage: storage}},
	}
	if broker != nil {
		components = append(components, component{name: componentBroker, p: broker})
	}
	return &HealthHandler{
		brokerType: brokerType,
		components: components,
		logger:     logger,
	}
}

// Drain переводит /health в 503 «shutting_down» без проверки зависимостей.
// Вызывается по SIGTERM, чтобы балансировщики перестали слать запросы до того,
// как HTTP-сервер закроется.
func (h *HealthHandler) Drain() {
	h.draining.Store(true)
}

// Check проверяет компоненты параллельно, у каждой проверки свой таймаут.
// Отвечает 200 со статусом "ok", если все доступны, и 503 со статусом "degraded", если нет.
// После Drain всегда отвечает 503 со статусом "shutting_down".
func (h *HealthHandler) Check(w http.ResponseWriter, r *http.Request) {
	if h.draining.Load() {
		httpx.WriteJSON(w, http.StatusServiceUnavailable, healthResponse{
			Status:     healthShuttingDown,
			BrokerType: h.brokerType,
			Components: map[string]string{},
		})
		return
	}

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

// livezResponse — ответ /livez: {"status":"ok"}.
type livezResponse struct {
	Status string `json:"status"`
}

// Live — проверка живости процесса (livenessProbe и startupProbe). Зависимости
// не проверяет: если упадёт БД, перезапуск подов не поможет, а только добавит
// нагрузки. Отвечает 200, пока процесс способен обслужить HTTP-запрос.
func Live(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, livezResponse{Status: healthOK})
}
