package middleware

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"

	"go-avatar-service/internal/httpx"
)

// Recoverer перехватывает панику в хендлере, логирует её со стеком
// и отвечает 500 в едином формате ошибки, а не пустым телом, как chi.Recoverer.
func Recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rvr := recover()
				if rvr == nil {
					return
				}
				// http.ErrAbortHandler — штатный способ прервать ответ, net/http обработает его сам.
				if err, ok := rvr.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rvr)
				}

				log.ErrorContext(r.Context(), "panic recovered",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", rvr,
					"stack", string(debug.Stack()),
				)
				httpx.WriteError(w, http.StatusInternalServerError, "Internal server error", "")
			}()

			next.ServeHTTP(w, r)
		})
	}
}
