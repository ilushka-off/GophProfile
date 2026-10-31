package services

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Значения лейбла status у avatars_uploads_total.
const (
	uploadSuccess  = "success"
	uploadRejected = "rejected" // файл не прошёл проверки: формат, размер, тип
	uploadError    = "error"    // сбой БД, S3 или брокера
)

// UploadDurationBuckets — границы avatars_upload_duration_seconds: загрузка
// включает чтение файла, запись в S3, БД и публикацию события.
var UploadDurationBuckets = []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Metrics — бизнес-метрики сервиса аватарок.
type Metrics struct {
	uploads        *prometheus.CounterVec
	uploadDuration prometheus.Histogram
	deletes        prometheus.Counter
}

// NewMetrics регистрирует метрики в reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		uploads: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "avatars_uploads_total",
			Help: "Avatar uploads by outcome: success, rejected (invalid file) or error (service failure).",
		}, []string{"status"}),
		uploadDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "avatars_upload_duration_seconds",
			Help:    "Duration of successful avatar uploads in seconds.",
			Buckets: UploadDurationBuckets,
		}),
		deletes: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "avatars_deletes_total",
			Help: "Avatars deleted by users.",
		}),
	}
	// Серии со всеми статусами видны с нуля: rate() по ним считается сразу.
	for _, status := range []string{uploadSuccess, uploadRejected, uploadError} {
		m.uploads.WithLabelValues(status)
	}
	reg.MustRegister(m.uploads, m.uploadDuration, m.deletes)
	return m
}

// observeUpload записывает исход загрузки, начатой в start. nil — метрики выключены.
func (m *Metrics) observeUpload(start time.Time, err error) {
	if m == nil {
		return
	}
	switch {
	case err == nil:
		m.uploads.WithLabelValues(uploadSuccess).Inc()
		m.uploadDuration.Observe(time.Since(start).Seconds())
	case IsClientError(err):
		m.uploads.WithLabelValues(uploadRejected).Inc()
	default:
		m.uploads.WithLabelValues(uploadError).Inc()
	}
}

// observeDelete считает успешное удаление.
func (m *Metrics) observeDelete(err error) {
	if m == nil || err != nil {
		return
	}
	m.deletes.Inc()
}

// Option — необязательная настройка AvatarService.
type Option func(*AvatarService)

// WithMetrics включает бизнес-метрики.
func WithMetrics(m *Metrics) Option {
	return func(s *AvatarService) { s.metrics = m }
}
