package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMaxBodySize(t *testing.T) {
	tooLarge := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
	}

	tests := []struct {
		name          string
		body          string
		contentLength int64 // -1 — chunked, длина неизвестна
		wantStatus    int
		wantCalled    bool
	}{
		{"within limit", "12345", 5, http.StatusOK, true},
		{"declared too large is rejected before the handler", "123456", 6, http.StatusRequestEntityTooLarge, false},
		{"chunked over limit fails on read", "123456789", -1, http.StatusRequestEntityTooLarge, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			h := MaxBodySize(5, tooLarge)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if _, err := io.ReadAll(r.Body); err != nil {
					var maxErr *http.MaxBytesError
					if !errors.As(err, &maxErr) {
						t.Errorf("read error = %v, want *http.MaxBytesError", err)
					}
					w.WriteHeader(http.StatusRequestEntityTooLarge)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			req.ContentLength = tt.contentLength
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if called != tt.wantCalled {
				t.Errorf("handler called = %v, want %v", called, tt.wantCalled)
			}
		})
	}
}
