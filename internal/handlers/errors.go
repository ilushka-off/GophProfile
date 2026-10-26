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
	status, resp := serviceErrorResponse(err, maxUploadSize)
	if status == http.StatusInternalServerError {
		logger.ErrorContext(r.Context(), "unhandled service error", "method", r.Method, "path", r.URL.Path, "err", err)
	}
	httpx.WriteJSON(w, status, resp)
}

// serviceErrorResponse сопоставляет ошибку сервиса со статусом и телом ответа.
// Всё неизвестное — 500 без подробностей. Общая для API и веб-интерфейса.
func serviceErrorResponse(err error, maxUploadSize int64) (int, httpx.ErrorResponse) {
	var verr *domain.ValidationError
	switch {
	case errors.As(err, &verr):
		return http.StatusBadRequest, httpx.ErrorResponse{Error: msgInvalidRequest, Details: verr.Details}
	case errors.Is(err, domain.ErrInvalidFormat):
		return http.StatusBadRequest, httpx.ErrorResponse{Error: msgInvalidFormat, Details: detailsInvalidFormat}
	case errors.Is(err, domain.ErrForbidden):
		return http.StatusForbidden, httpx.ErrorResponse{Error: msgForbidden, Details: detailsForbidden}
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, httpx.ErrorResponse{Error: msgNotFound}
	case errors.Is(err, domain.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, httpx.ErrorResponse{Error: msgTooLarge, MaxSize: maxUploadSize}
	default:
		return http.StatusInternalServerError, httpx.ErrorResponse{Error: msgInternal}
	}
}

// writeTooLarge пишет 413 в формате ТЗ: {"error":"File too large","max_size":N}.
// Вынесен отдельно, потому что лимит обычно срабатывает ещё в хендлере
// (http.MaxBytesReader), до вызова сервиса.
func writeTooLarge(w http.ResponseWriter, maxUploadSize int64) {
	httpx.WriteJSON(w, http.StatusRequestEntityTooLarge, httpx.ErrorResponse{Error: msgTooLarge, MaxSize: maxUploadSize})
}
