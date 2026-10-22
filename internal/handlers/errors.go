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
// Текст err клиенту не уходит: в обёрнутой ошибке видна внутренняя цепочка вызовов.
// maxUploadSize нужен для ответа 413.
func writeServiceError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error, maxUploadSize int64) {
	var verr *domain.ValidationError
	switch {
	case errors.As(err, &verr):
		httpx.WriteError(w, http.StatusBadRequest, msgInvalidRequest, verr.Details)
	case errors.Is(err, domain.ErrInvalidFormat):
		httpx.WriteError(w, http.StatusBadRequest, msgInvalidFormat, detailsInvalidFormat)
	case errors.Is(err, domain.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, msgForbidden, detailsForbidden)
	case errors.Is(err, domain.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, msgNotFound, "")
	case errors.Is(err, domain.ErrTooLarge):
		writeTooLarge(w, maxUploadSize)
	default:
		logger.ErrorContext(r.Context(), "unhandled service error", "method", r.Method, "path", r.URL.Path, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, msgInternal, "")
	}
}

// writeTooLarge пишет 413 в формате ТЗ: {"error":"File too large","max_size":N}.
// Вынесен отдельно, потому что лимит обычно срабатывает ещё в хендлере
// (http.MaxBytesReader), до вызова сервиса.
func writeTooLarge(w http.ResponseWriter, maxUploadSize int64) {
	httpx.WriteJSON(w, http.StatusRequestEntityTooLarge, httpx.ErrorResponse{Error: msgTooLarge, MaxSize: maxUploadSize})
}
