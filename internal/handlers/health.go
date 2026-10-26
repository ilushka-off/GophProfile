package handlers

import (
	"context"
	"errors"
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

// HealthHandler обслуживает проверку живости: отвечает о доступности сервиса и его зависимостей.
type HealthHandler struct {
	brokerType string
	components []component
	logger     *slog.Logger
}

// NewHealthHandler создаёт HealthHandler, который проверяет БД, S3 и брокер.
// brokerType — значение BROKER из конфига (rabbitmq или kafka).
func NewHealthHandler(db pinger, storage bucketChecker, broker pinger, brokerType string, logger *slog.Logger) *HealthHandler {
	return &HealthHandler{
		brokerType: brokerType,
		components: []component{
			{name: componentDatabase, p: db},
			{name: componentStorage, p: storagePinger{storage: storage}},
			{name: componentBroker, p: broker},
		},
		logger: logger,
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
