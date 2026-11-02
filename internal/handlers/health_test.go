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

// fakeBucket реализует bucketChecker.
type fakeBucket struct {
	exists bool
	err    error
}

func (f fakeBucket) BucketExists(context.Context) (bool, error) {
	return f.exists, f.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// healthyHandler — HealthHandler, у которого все зависимости доступны.
func healthyHandler() *HealthHandler {
	return NewHealthHandler(fakePinger{}, fakeBucket{exists: true}, fakePinger{}, "rabbitmq", discardLogger())
}

func TestHealthHandlerCheck(t *testing.T) {
	errRefused := errors.New("dial tcp: connection refused")

	tests := []struct {
		name       string
		db         pinger
		storage    bucketChecker
		broker     pinger
		wantStatus int
		wantBody   string
	}{
		{
			name:       "all reachable",
			db:         fakePinger{},
			storage:    fakeBucket{exists: true},
			broker:     fakePinger{},
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"ok","broker_type":"rabbitmq","components":{"broker":"ok","database":"ok","storage":"ok"}}`,
		},
		{
			name:       "database unavailable",
			db:         fakePinger{err: errRefused},
			storage:    fakeBucket{exists: true},
			broker:     fakePinger{},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"degraded","broker_type":"rabbitmq","components":{"broker":"ok","database":"unavailable","storage":"ok"}}`,
		},
		{
			name:       "database hangs past timeout",
			db:         hangingPinger{},
			storage:    fakeBucket{exists: true},
			broker:     fakePinger{},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"degraded","broker_type":"rabbitmq","components":{"broker":"ok","database":"unavailable","storage":"ok"}}`,
		},
		{
			name:       "storage unavailable",
			db:         fakePinger{},
			storage:    fakeBucket{err: errRefused},
			broker:     fakePinger{},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"degraded","broker_type":"rabbitmq","components":{"broker":"ok","database":"ok","storage":"unavailable"}}`,
		},
		{
			name:       "bucket missing",
			db:         fakePinger{},
			storage:    fakeBucket{exists: false},
			broker:     fakePinger{},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"degraded","broker_type":"rabbitmq","components":{"broker":"ok","database":"ok","storage":"unavailable"}}`,
		},
		{
			name:       "broker unavailable",
			db:         fakePinger{},
			storage:    fakeBucket{exists: true},
			broker:     fakePinger{err: errRefused},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"degraded","broker_type":"rabbitmq","components":{"broker":"unavailable","database":"ok","storage":"ok"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHealthHandler(tt.db, tt.storage, tt.broker, "rabbitmq", discardLogger())

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

func TestHealthHandlerWithoutBroker(t *testing.T) {
	h := NewHealthHandler(fakePinger{}, fakeBucket{exists: true}, nil, "kafka", discardLogger())

	rec := httptest.NewRecorder()
	h.Check(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	want := `{"status":"ok","broker_type":"kafka","components":{"database":"ok","storage":"ok"}}` + "\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestHealthHandlerDrain(t *testing.T) {
	// После Drain зависимости не опрашиваются: даже зависшая БД не задерживает ответ.
	h := NewHealthHandler(hangingPinger{}, fakeBucket{exists: true}, fakePinger{}, "rabbitmq", discardLogger())
	h.Drain()

	rec := httptest.NewRecorder()
	start := time.Now()
	h.Check(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if elapsed := time.Since(start); elapsed > healthCheckTimeout/2 {
		t.Errorf("draining check took %v, want an immediate answer", elapsed)
	}

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	want := `{"status":"shutting_down","broker_type":"rabbitmq","components":{}}` + "\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestLive(t *testing.T) {
	rec := httptest.NewRecorder()
	Live(rec, httptest.NewRequest(http.MethodGet, "/livez", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), `{"status":"ok"}`+"\n"; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}
