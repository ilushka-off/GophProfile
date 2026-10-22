package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakePinger реализует pinger и возвращает заданную ошибку.
type fakePinger struct {
	err error
}

func (f fakePinger) Ping(_ context.Context) error {
	return f.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHealthHandlerCheck(t *testing.T) {
	tests := []struct {
		name       string
		pingErr    error
		wantStatus int
		wantBody   map[string]string
	}{
		{
			name:       "database reachable",
			pingErr:    nil,
			wantStatus: http.StatusOK,
			wantBody:   map[string]string{"status": "ok"},
		},
		{
			name:       "database unavailable",
			pingErr:    errors.New("dial tcp: connection refused"),
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   map[string]string{"error": msgUnavailable, "details": "database is unavailable"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHealthHandler(fakePinger{err: tt.pingErr}, discardLogger())

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/health", nil)

			h.Check(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if len(body) != len(tt.wantBody) {
				t.Errorf("body = %v, want %v", body, tt.wantBody)
			}
			for k, want := range tt.wantBody {
				if body[k] != want {
					t.Errorf("body[%q] = %q, want %q", k, body[k], want)
				}
			}
		})
	}
}
