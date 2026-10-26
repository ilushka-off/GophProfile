package middleware

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Что разрешено браузеру при запросах с другого origin.
var (
	corsMethods       = []string{http.MethodGet, http.MethodPost, http.MethodDelete}
	corsHeaders       = []string{"Content-Type", "X-User-ID", "If-None-Match"}
	corsExposeHeaders = []string{"ETag", "Retry-After"}
)

// corsMaxAge — сколько браузер кеширует ответ на preflight.
const corsMaxAge = 10 * time.Minute

// CORS разрешает запросы из браузера с origins из allowed. "*" в списке —
// любой origin. Пустой список — CORS выключен: заголовки не ставятся,
// и браузер пускает только запросы с того же origin. Preflight-запрос
// (OPTIONS с Access-Control-Request-Method) обрабатывается здесь же и
// до хендлеров не доходит.
func CORS(allowed []string) func(http.Handler) http.Handler {
	if len(allowed) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	allowAll := slices.Contains(allowed, "*")
	methods := strings.Join(corsMethods, ", ")
	headers := strings.Join(corsHeaders, ", ")
	expose := strings.Join(corsExposeHeaders, ", ")
	maxAge := strconv.Itoa(int(corsMaxAge.Seconds()))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""

			h := w.Header()
			// Ответ зависит от Origin — кеши должны это учитывать.
			h.Add("Vary", "Origin")
			if preflight {
				h.Add("Vary", "Access-Control-Request-Method")
				h.Add("Vary", "Access-Control-Request-Headers")
			}

			if origin == "" || (!allowAll && !slices.Contains(allowed, origin)) {
				if preflight {
					// Без CORS-заголовков браузер сам отклонит запрос.
					w.WriteHeader(http.StatusNoContent)
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			if allowAll {
				h.Set("Access-Control-Allow-Origin", "*")
			} else {
				h.Set("Access-Control-Allow-Origin", origin)
			}

			if preflight {
				h.Set("Access-Control-Allow-Methods", methods)
				h.Set("Access-Control-Allow-Headers", headers)
				h.Set("Access-Control-Max-Age", maxAge)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			h.Set("Access-Control-Expose-Headers", expose)
			next.ServeHTTP(w, r)
		})
	}
}
