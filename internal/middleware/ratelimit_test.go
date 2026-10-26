package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestIPLimiter(t *testing.T) {
	now := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	l := newIPLimiter(rate.Limit(1), 2, func() time.Time { return now })

	// Ведро на 2 запроса: третий подряд не проходит.
	for i, want := range []bool{true, true, false} {
		if got := l.allow("10.0.0.1"); got != want {
			t.Errorf("request %d: allow = %v, want %v", i+1, got, want)
		}
	}
	// У другого IP своё ведро.
	if !l.allow("10.0.0.2") {
		t.Error("another IP must not be limited")
	}
	// Через секунду ведро пополнилось на один запрос.
	now = now.Add(time.Second)
	if !l.allow("10.0.0.1") {
		t.Error("bucket did not refill after a second")
	}

	// Молчащие дольше clientTTL клиенты забываются.
	now = now.Add(clientTTL + time.Second)
	l.allow("10.0.0.3")
	if _, ok := l.clients["10.0.0.1"]; ok {
		t.Error("idle client was not swept")
	}
	if len(l.clients) != 1 {
		t.Errorf("clients = %d, want 1", len(l.clients))
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	tooMany := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	t.Run("limits by remote ip", func(t *testing.T) {
		h := RateLimit(0.5, 1, tooMany)(ok)

		send := func(remote string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = remote
			req.Header.Set("X-Forwarded-For", "1.2.3.4") // не влияет: заголовку не доверяем
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			return rec
		}

		if rec := send("192.0.2.1:1111"); rec.Code != http.StatusOK {
			t.Fatalf("first request: %d", rec.Code)
		}
		rec := send("192.0.2.1:2222") // другой порт, тот же IP
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("second request: %d, want 429", rec.Code)
		}
		if got := rec.Header().Get("Retry-After"); got != "2" {
			t.Errorf("Retry-After = %q, want 2", got)
		}
		if rec := send("192.0.2.9:1111"); rec.Code != http.StatusOK {
			t.Errorf("other ip: %d, want 200", rec.Code)
		}
	})

	t.Run("zero rps disables the limit", func(t *testing.T) {
		h := RateLimit(0, 1, tooMany)(ok)
		for range 100 {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
		}
	})
}

func TestClientIP(t *testing.T) {
	tests := map[string]string{
		"192.0.2.1:1234":    "192.0.2.1",
		"[2001:db8::1]:443": "2001:db8::1",
		"no-port":           "no-port",
	}
	for remote, want := range tests {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = remote
		if got := clientIP(req); got != want {
			t.Errorf("clientIP(%q) = %q, want %q", remote, got, want)
		}
	}
}
