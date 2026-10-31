package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"go-avatar-service/internal/observability"
)

// Access-лог запроса несёт тот же trace_id, что и ответ, — по нему лог
// находится из трейса и наоборот.
func TestAccessLogHasTraceID(t *testing.T) {
	recordSpans(t)
	var buf bytes.Buffer
	logger := observability.NewLogger(&buf, slog.LevelInfo, observability.Service{Name: observability.ServiceServer})

	r := chi.NewRouter()
	r.Use(Tracing())
	r.Use(Logger(logger))
	r.Get("/api/v1/avatars/{avatar_id}", func(http.ResponseWriter, *http.Request) {})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/1", nil))

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("log is not JSON: %v", err)
	}
	if entry["trace_id"] == nil || entry["trace_id"] != rec.Header().Get(HeaderTraceID) {
		t.Errorf("trace_id = %v, %s = %q", entry["trace_id"], HeaderTraceID, rec.Header().Get(HeaderTraceID))
	}
	for _, key := range []string{"route", "status", "duration_ms", "service"} {
		if _, ok := entry[key]; !ok {
			t.Errorf("access log has no %s: %v", key, entry)
		}
	}
}
