package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

// Logger логирует каждый запрос: метод, маршрут, путь, статус и длительность.
// Маршрут берётся из chi после обработки — до неё RoutePattern ещё не заполнен.
func Logger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			// Хендлер ничего не записал — net/http ответит 200.
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}

			var route string
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				route = rctx.RoutePattern()
			}

			log.InfoContext(r.Context(), "http request",
				"request_id", chimw.GetReqID(r.Context()),
				"method", r.Method,
				"route", route,
				"path", r.URL.Path,
				"status", status,
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}
