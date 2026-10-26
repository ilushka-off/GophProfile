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
func NewRouter(logger *slog.Logger, healthHandler *HealthHandler, avatarHandler *AvatarHandler, webHandler *WebHandler) http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	// Logger снаружи Recoverer, чтобы паника попала в лог как запрос со статусом 500.
	r.Use(middleware.Logger(logger))
	r.Use(middleware.Recoverer(logger))

	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, http.StatusNotFound, msgRouteNotFound, "")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, http.StatusMethodNotAllowed, msgMethodNotAllowed, "")
	})

	r.Get("/health", healthHandler.Check)

	r.Get("/", webHandler.Root)
	r.Route("/web", func(r chi.Router) {
		r.Get("/upload", webHandler.UploadForm)
		r.Post("/upload", webHandler.Upload)
		r.Get("/gallery/{user_id}", webHandler.Gallery)
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/avatars", avatarHandler.Upload)
		r.Get("/avatars/{avatar_id}", avatarHandler.Get)
		r.Get("/avatars/{avatar_id}/metadata", avatarHandler.Metadata)
		r.Delete("/avatars/{avatar_id}", avatarHandler.Delete)
		r.Get("/users/{user_id}/avatar", avatarHandler.GetCurrent)
		r.Get("/users/{user_id}/avatars", avatarHandler.List)
		r.Delete("/users/{user_id}/avatar", avatarHandler.DeleteCurrent)
	})

	return r
}
