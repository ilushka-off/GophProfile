package middleware

import "net/http"

// MaxBodySize ограничивает тело запроса limit байтами. Если клиент заранее
// заявил больший Content-Length, отвечает tooLarge, не читая тело. Иначе
// оборачивает тело в http.MaxBytesReader: чтение сверх лимита (например,
// у chunked-запроса без Content-Length) вернёт *http.MaxBytesError, и хендлер
// ответит 413 сам.
func MaxBodySize(limit int64, tooLarge http.HandlerFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > limit {
				// Тело не читаем: соединение закроется, чтобы не принимать лишние мегабайты.
				w.Header().Set("Connection", "close")
				tooLarge(w, r)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}
