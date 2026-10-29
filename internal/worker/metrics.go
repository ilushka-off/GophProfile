package worker

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"go-avatar-service/internal/broker"
)

// Значения лейбла status у avatars_processing_total.
const (
	processingSuccess = "success"
	processingError   = "error" // сообщение ушло в DLQ
)

// ProcessingDurationBuckets — границы avatars_processing_duration_seconds:
// миниатюры большой картинки и повторы с backoff (до 7 с) тоже должны попасть.
var ProcessingDurationBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

// ProcessingLagBuckets — границы avatars_processing_lag_seconds: от «сразу»
// до очереди, накопившейся за время простоя worker'а.
var ProcessingLagBuckets = []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 15, 60, 300, 900}

// Metrics — метрики обработки событий.
type Metrics struct {
	processed *prometheus.CounterVec
	duration  *prometheus.HistogramVec
	lag       *prometheus.HistogramVec
}

// NewMetrics регистрирует метрики в reg. Лейбл type — тип события.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		processed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "avatars_processing_total",
			Help: "Processed broker events by type and outcome: success or error (sent to the DLQ).",
		}, []string{"type", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "avatars_processing_duration_seconds",
			Help:    "Time to process a broker event in seconds, retries included.",
			Buckets: ProcessingDurationBuckets,
		}, []string{"type"}),
		lag: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "avatars_processing_lag_seconds",
			Help:    "Time from publishing an event to the start of its processing in seconds.",
			Buckets: ProcessingLagBuckets,
		}, []string{"type"}),
	}
	reg.MustRegister(m.processed, m.duration, m.lag)
	return m
}

// Instrument — внешняя обёртка над обработчиком: считает каждое событие
// с итоговым исходом (после всех повторов) и задержку от публикации.
// Обработка, прерванная остановкой worker'а, не считается: сообщение придёт снова.
func (m *Metrics) Instrument(next broker.Handler) broker.Handler {
	return func(ctx context.Context, msg broker.Message) error {
		start := time.Now()
		if !msg.PublishedAt.IsZero() {
			// Часы publisher'а могут убегать вперёд — отрицательная задержка бессмысленна.
			m.lag.WithLabelValues(msg.Type).Observe(max(start.Sub(msg.PublishedAt).Seconds(), 0))
		}

		err := next(ctx, msg)
		if ctx.Err() != nil {
			return err
		}

		status := processingSuccess
		if err != nil {
			status = processingError
		}
		m.processed.WithLabelValues(msg.Type, status).Inc()
		m.duration.WithLabelValues(msg.Type).Observe(time.Since(start).Seconds())
		return err
	}
}
