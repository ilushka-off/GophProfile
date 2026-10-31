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

			log.InfoContext(r.Context(), "http request",
				"request_id", chimw.GetReqID(r.Context()),
				"method", r.Method,
				"route", routePattern(r),
				"path", r.URL.Path,
				"status", status,
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

// routePattern возвращает шаблон маршрута chi, например /api/v1/avatars/{avatar_id}.
// Пусто, если запрос не попал ни в маршрут, ни в подроутер или ещё не прошёл
// роутинг; для неизвестного пути внутри подроутера — его шаблон, /api/v1/*.
func routePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		return rctx.RoutePattern()
	}
	return ""
}
