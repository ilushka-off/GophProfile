package middleware

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"
)

// routeUnmatched — значение лейбла route для запросов, не дошедших ни до
// одного маршрута или подроутера: путь в лейбл не попадает, иначе сканер URL
// создал бы тысячи серий. Неизвестный путь внутри подроутера (/api/v1/nope)
// получает шаблон подроутера с wildcard — /api/v1/* — так chi заполняет
// RoutePattern. Это тоже одно значение на подроутер, кардинальность не растёт.
const routeUnmatched = "unmatched"

// methodOther — лейбл method для нестандартных методов, по той же причине.
const methodOther = "OTHER"

// knownMethods — методы, которые попадают в лейбл как есть.
var knownMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
	http.MethodDelete, http.MethodOptions,
}

// HTTPDurationBuckets — границы гистограммы длительности запросов в секундах:
// от быстрых GET метаданных до загрузки 10 MB.
var HTTPDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// HTTPMetrics — RED-метрики HTTP: rate, errors (по лейблу status), duration.
type HTTPMetrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inFlight prometheus.Gauge
}

// NewHTTPMetrics регистрирует метрики http_requests_total,
// http_request_duration_seconds и http_requests_in_flight в reg.
func NewHTTPMetrics(reg prometheus.Registerer) *HTTPMetrics {
	labels := []string{"method", "route", "status"}
	m := &HTTPMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests by method, route template and status code.",
		}, labels),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request duration in seconds by method, route template and status code.",
			Buckets: HTTPDurationBuckets,
		}, labels),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "HTTP requests being served right now.",
		}),
	}
	reg.MustRegister(m.requests, m.duration, m.inFlight)
	return m
}

// Middleware считает каждый запрос, кроме путей из skip. Лейбл route — шаблон
// маршрута chi (/api/v1/avatars/{avatar_id}), а не путь. Если запрос идёт
// внутри записываемого трейса, к наблюдению длительности прикрепляется
// exemplar с trace_id: из графика можно перейти к трейсу.
func (m *HTTPMetrics) Middleware(skip ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if slices.Contains(skip, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			m.inFlight.Inc()
			defer m.inFlight.Dec()
			start := time.Now()
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

			// Паника пролетит дальше к внешнему обработчику, но запрос всё равно
			// посчитан: Recoverer стоит внутри и превращает её в 500.
			next.ServeHTTP(ww, r)

			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			route := routePattern(r)
			if route == "" {
				route = routeUnmatched
			}
			method := r.Method
			if !slices.Contains(knownMethods, method) {
				method = methodOther
			}

			labels := prometheus.Labels{"method": method, "route": route, "status": strconv.Itoa(status)}
			m.requests.With(labels).Inc()
			observe(r, m.duration.With(labels), time.Since(start).Seconds())
		})
	}
}

// observe записывает значение с exemplar trace_id, если запрос в записываемом трейсе.
func observe(r *http.Request, o prometheus.Observer, v float64) {
	if sc := trace.SpanContextFromContext(r.Context()); sc.IsSampled() {
		if eo, ok := o.(prometheus.ExemplarObserver); ok {
			eo.ObserveWithExemplar(v, prometheus.Labels{"trace_id": sc.TraceID().String()})
			return
		}
	}
	o.Observe(v)
}
