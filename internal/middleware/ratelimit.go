package middleware

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// clientTTL — через сколько без запросов клиент забывается. Ведро за это
// время всё равно наполнилось бы до конца, так что ограничение не теряется.
const clientTTL = 3 * time.Minute

// RateLimit ограничивает частоту запросов с одного IP (token bucket): в ведро
// помещается burst запросов, пополняется оно со скоростью rps в секунду.
// Сверх лимита отвечает tooMany с заголовком Retry-After. rps = 0 — без
// ограничения. IP берётся из RemoteAddr: заголовкам X-Forwarded-For без
// доверенного прокси верить нельзя, их подделает любой клиент.
func RateLimit(rps float64, burst int, tooMany http.HandlerFunc) func(http.Handler) http.Handler {
	if rps <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	l := newIPLimiter(rate.Limit(rps), burst, time.Now)
	retryAfter := strconv.Itoa(max(1, int(1/rps+0.5)))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.allow(clientIP(r)) {
				w.Header().Set("Retry-After", retryAfter)
				tooMany(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ipLimiter хранит по ведру на каждый IP и раз в clientTTL выбрасывает
// давно молчащих клиентов, чтобы карта не росла бесконечно.
type ipLimiter struct {
	limit rate.Limit
	burst int
	now   func() time.Time

	mu        sync.Mutex
	clients   map[string]*client
	lastSweep time.Time
}

type client struct {
	limiter *rate.Limiter
	seen    time.Time
}

func newIPLimiter(limit rate.Limit, burst int, now func() time.Time) *ipLimiter {
	return &ipLimiter{limit: limit, burst: burst, now: now, clients: map[string]*client{}, lastSweep: now()}
}

func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if now.Sub(l.lastSweep) > clientTTL {
		for key, c := range l.clients {
			if now.Sub(c.seen) > clientTTL {
				delete(l.clients, key)
			}
		}
		l.lastSweep = now
	}

	c, ok := l.clients[ip]
	if !ok {
		c = &client{limiter: rate.NewLimiter(l.limit, l.burst)}
		l.clients[ip] = c
	}
	c.seen = now
	return c.limiter.AllowN(now, 1)
}

// clientIP — IP из RemoteAddr без порта.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
