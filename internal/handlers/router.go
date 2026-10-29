package handlers

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"go-avatar-service/internal/httpx"
	"go-avatar-service/internal/middleware"
)

// pathHealth — служебный путь вне API: его не трейсят и не считают в метриках
// запросов. /metrics здесь нет: он на отдельном адресе (METRICS_ADDR).
const pathHealth = "/health"

// RouterOptions — настройки защитных middleware.
type RouterOptions struct {
	// RateLimitRPS и RateLimitBurst — лимит запросов с одного IP к /api/v1
	// и /web. RateLimitRPS = 0 — без ограничения.
	RateLimitRPS   float64
	RateLimitBurst int
	// CORSAllowedOrigins — origins, которым браузер разрешает запросы. Пусто — CORS выключен.
	CORSAllowedOrigins []string
	// HTTPMetrics считает RED-метрики запросов. nil — не считать.
	HTTPMetrics *middleware.HTTPMetrics
}

// NewRouter собирает HTTP-роутер: общие middleware, служебные маршруты
// /health и /web вне версии API и группу /api/v1 для эндпоинтов сервиса.
func NewRouter(logger *slog.Logger, opts RouterOptions, healthHandler *HealthHandler, avatarHandler *AvatarHandler, webHandler *WebHandler) http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	// Трейсинг первым: спан из контекста нужен логам (trace_id) и всем ниже.
	r.Use(middleware.Tracing(pathHealth))
	if opts.HTTPMetrics != nil {
		r.Use(opts.HTTPMetrics.Middleware(pathHealth))
	}
	// Logger снаружи Recoverer, чтобы паника попала в лог как запрос со статусом 500.
	r.Use(middleware.Logger(logger))
	r.Use(middleware.Recoverer(logger))
	// CORS до роутинга: на preflight OPTIONS chi иначе ответил бы 405.
	r.Use(middleware.CORS(opts.CORSAllowedOrigins))

	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, http.StatusNotFound, msgRouteNotFound, "")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, http.StatusMethodNotAllowed, msgMethodNotAllowed, "")
	})

	// /health не ограничивается: его дёргает healthcheck docker compose.
	r.Get(pathHealth, healthHandler.Check)

	// Один лимитер на API и веб: у клиента одно ведро на весь сервис.
	rateLimit := middleware.RateLimit(opts.RateLimitRPS, opts.RateLimitBurst, func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, http.StatusTooManyRequests, msgTooManyRequests, "")
	})

	// Тело запроса ограничено лимитом файла плюс запас на multipart: большой
	// запрос отклоняется по Content-Length, не доходя до хендлера.
	r.Get("/", webHandler.Root)
	r.Route("/web", func(r chi.Router) {
		r.Use(rateLimit)
		r.Use(middleware.MaxBodySize(webHandler.maxUploadSize+multipartOverhead, func(w http.ResponseWriter, r *http.Request) {
			webHandler.renderUploadError(w, r, "", http.StatusRequestEntityTooLarge, webHandler.tooLargeMessage())
		}))
		r.Get("/upload", webHandler.UploadForm)
		r.Post("/upload", webHandler.Upload)
		r.Get("/gallery/{user_id}", webHandler.Gallery)
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(rateLimit)
		r.Use(middleware.MaxBodySize(avatarHandler.maxUploadSize+multipartOverhead, func(w http.ResponseWriter, _ *http.Request) {
			writeTooLarge(w, avatarHandler.maxUploadSize)
		}))
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
