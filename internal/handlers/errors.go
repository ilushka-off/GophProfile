package handlers

import (
	"errors"
	"log/slog"
	"net/http"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/httpx"
)

// writeServiceError переводит ошибку сервиса в HTTP-ответ. Это граница, где
// ошибка логируется ровно один раз — и только 500, потому что 4xx не ошибка сервера.
func writeServiceError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	var verr *domain.ValidationError
	switch {
	case errors.As(err, &verr):
		httpx.WriteError(w, http.StatusBadRequest, msgValidation, verr.Details)
	case errors.Is(err, domain.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, msgNotFound, err.Error())
	case errors.Is(err, domain.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, msgForbidden, err.Error())
	case errors.Is(err, domain.ErrInvalidFormat):
		httpx.WriteError(w, http.StatusUnsupportedMediaType, msgInvalidFormat, err.Error())
	case errors.Is(err, domain.ErrTooLarge):
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, msgTooLarge, err.Error())
	default:
		logger.ErrorContext(r.Context(), "unhandled service error", "method", r.Method, "path", r.URL.Path, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, msgInternal, "")
	}
}
