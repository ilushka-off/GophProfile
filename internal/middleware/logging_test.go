package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestLoggerLogsRequest(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantStatus float64 // JSON numbers decode to float64
	}{
		{
			name: "explicit WriteHeader is logged",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "body without WriteHeader defaults to 200",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("ok"))
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "panic is logged as 500",
			handler: func(http.ResponseWriter, *http.Request) {
				panic("boom")
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, nil))

			// Прогоняем через настоящий chi-роутер, иначе RoutePattern() пуст:
			// паттерн попадает в RouteContext только при матчинге маршрута.
			r := chi.NewRouter()
			r.Use(Logger(logger))
			r.Use(Recoverer(slog.New(slog.DiscardHandler)))
			r.Get("/api/v1/avatars/{id}", tt.handler)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/1", nil)
			r.ServeHTTP(rec, req)

			var entry map[string]any
			if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
				t.Fatalf("parse log line %q: %v", buf.String(), err)
			}
			if entry["status"] != tt.wantStatus {
				t.Errorf("status = %v, want %v", entry["status"], tt.wantStatus)
			}
			if entry["method"] != http.MethodGet {
				t.Errorf("method = %v, want %s", entry["method"], http.MethodGet)
			}
			if entry["path"] != "/api/v1/avatars/1" {
				t.Errorf("path = %v, want /api/v1/avatars/1", entry["path"])
			}
			if entry["route"] != "/api/v1/avatars/{id}" {
				t.Errorf("route = %v, want /api/v1/avatars/{id}", entry["route"])
			}
		})
	}
}
