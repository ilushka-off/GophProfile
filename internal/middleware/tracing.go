package middleware

import (
	"net/http"
	"slices"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// HeaderTraceID — заголовок ответа с trace_id запроса: по нему клиент или
// поддержка найдут трейс в Jaeger и логи в Loki.
const HeaderTraceID = "X-Trace-ID"

// Tracing начинает серверный спан на каждый запрос, кроме путей из skip
// (служебные /health и /metrics только засоряли бы трейсы). Входящий
// traceparent продолжается. Спан называется по шаблону маршрута chi —
// "GET /api/v1/avatars/{avatar_id}", а не по пути с id, иначе имён были бы
// миллионы. Ответы 5xx помечают спан ошибкой (это делает otelhttp).
func Tracing(skip ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		named := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			span := trace.SpanFromContext(r.Context())
			// Заголовок ставится до хендлера: после WriteHeader его уже не добавить.
			if sc := span.SpanContext(); sc.IsValid() {
				w.Header().Set(HeaderTraceID, sc.TraceID().String())
			}

			next.ServeHTTP(w, r)

			// Шаблон маршрута chi заполняет только после роутинга.
			if route := routePattern(r); route != "" {
				span.SetName(spanName(r))
				span.SetAttributes(semconv.HTTPRoute(route))
			}
		})

		return otelhttp.NewHandler(named, "",
			otelhttp.WithFilter(func(r *http.Request) bool {
				return !slices.Contains(skip, r.URL.Path)
			}),
			// otelhttp зовёт форматтер при старте спана и ещё раз после хендлера,
			// если chi заполнил r.Pattern, — имя должно совпадать с нашим.
			otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
				return spanName(r)
			}),
		)
	}
}

// spanName — "METHOD /route/{param}". До роутинга маршрут неизвестен,
// и спан называют по методу, как советуют семантические соглашения OTel.
func spanName(r *http.Request) string {
	if route := routePattern(r); route != "" {
		return r.Method + " " + route
	}
	return r.Method
}
