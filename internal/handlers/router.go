package handlers

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"go-avatar-service/internal/httpx"
	"go-avatar-service/internal/middleware"
)

// NewRouter собирает HTTP-роутер: общие middleware, служебные маршруты
// /health и /web вне версии API и группу /api/v1 для эндпоинтов сервиса.
// webDir — каталог со статикой веб-интерфейса.
func NewRouter(logger *slog.Logger, healthHandler *HealthHandler, webDir string) http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	// Logger снаружи Recoverer, чтобы паника попала в лог как запрос со статусом 500.
	r.Use(middleware.Logger(logger))
	r.Use(chimw.Recoverer)

	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, http.StatusNotFound, msgNotFound, "route not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed", "")
	})

	r.Get("/health", healthHandler.Check)

	r.Get("/web", http.RedirectHandler("/web/", http.StatusMovedPermanently).ServeHTTP)
	r.Handle("/web/*", http.StripPrefix("/web/", http.FileServer(http.Dir(webDir))))

	r.Route("/api/v1", func(r chi.Router) {
		// Эндпоинты аватаров добавляются в следующих тикетах.
	})

	return r
}
