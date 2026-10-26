package middleware

import (
	"io"
	"net/http"
)

// drainFactor — тело до drainFactor × limit дочитывается перед ответом 413.
const drainFactor = 4

// MaxBodySize ограничивает тело запроса limit байтами. Если клиент заранее
// заявил больший Content-Length, хендлер не вызывается: отвечает tooLarge.
// Иначе тело оборачивается в http.MaxBytesReader: чтение сверх лимита
// (например, у chunked-запроса без Content-Length) вернёт *http.MaxBytesError,
// и хендлер ответит 413 сам.
func MaxBodySize(limit int64, tooLarge http.HandlerFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > limit {
				rejectBody(w, r, limit)
				tooLarge(w, r)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

// rejectBody готовит соединение к ответу на слишком большой запрос. Многие
// клиенты и прокси (в том числе проброс портов Docker Desktop) не читают
// ответ, пока не отправили запрос целиком, и зависают, если сервер ответил
// и закрыл соединение раньше. Поэтому не слишком большое тело дочитывается
// и выбрасывается — в память и на диск оно не попадает. Совсем большое не
// читается: соединение закрывается после ответа.
func rejectBody(w http.ResponseWriter, r *http.Request, limit int64) {
	if r.ContentLength > drainFactor*limit {
		w.Header().Set("Connection", "close")
		return
	}
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		w.Header().Set("Connection", "close")
	}
}
