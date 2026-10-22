package handlers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakePinger реализует pinger и возвращает заданную ошибку.
type fakePinger struct {
	err error
}

func (f fakePinger) Ping(_ context.Context) error {
	return f.err
}

// hangingPinger не отвечает, пока не истечёт контекст, — как зависшая база.
type hangingPinger struct{}

func (hangingPinger) Ping(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHealthHandlerCheck(t *testing.T) {
	tests := []struct {
		name       string
		db         pinger
		wantStatus int
		wantBody   string
	}{
		{
			name:       "database reachable",
			db:         fakePinger{},
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"ok","broker_type":"rabbitmq","components":{"database":"ok"}}`,
		},
		{
			name:       "database unavailable",
			db:         fakePinger{err: errors.New("dial tcp: connection refused")},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"degraded","broker_type":"rabbitmq","components":{"database":"unavailable"}}`,
		},
		{
			name:       "database hangs past timeout",
			db:         hangingPinger{},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"degraded","broker_type":"rabbitmq","components":{"database":"unavailable"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHealthHandler(tt.db, "rabbitmq", discardLogger())

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/health", nil)

			start := time.Now()
			h.Check(rec, req)
			if elapsed := time.Since(start); elapsed > 2*healthCheckTimeout {
				t.Errorf("check took %v, want at most about %v", elapsed, healthCheckTimeout)
			}

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			if got := rec.Body.String(); got != tt.wantBody+"\n" {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
		})
	}
}
