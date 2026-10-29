package observability

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Таймауты отдельного HTTP-сервера метрик worker'а.
const (
	metricsReadHeaderTimeout = 5 * time.Second
	metricsShutdownTimeout   = 5 * time.Second
)

// NewRegistry создаёт собственный реестр метрик (а не глобальный
// prometheus.DefaultRegisterer, куда могут писать зависимости) с метриками
// Go runtime и процесса: горутины, heap, GC, CPU, память, файловые дескрипторы.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return reg
}

// MetricsHandler отдаёт метрики реестра. Формат OpenMetrics включается, если
// его просит Prometheus: только в нём передаются exemplars с trace_id.
func MetricsHandler(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
		Registry:          reg, // ошибки самого /metrics тоже станут метрикой
	})
}

// ServeMetrics поднимает HTTP-сервер с /metrics на addr и держит его до
// отмены ctx. Нужен worker'у: своего HTTP-сервера у него нет.
func ServeMetrics(ctx context.Context, addr string, reg *prometheus.Registry) error {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", MetricsHandler(reg))
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: metricsReadHeaderTimeout}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("serve metrics: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), metricsShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown metrics server: %w", err)
	}
	return nil
}
