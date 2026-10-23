package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-avatar-service/internal/domain"
)

func TestWriteServiceError(t *testing.T) {
	const maxUploadSize = 10 << 20

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{
			name:       "validation",
			err:        &domain.ValidationError{Details: "user_id is required"},
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error":"Invalid request","details":"user_id is required"}`,
		},
		{
			name:       "invalid format",
			err:        fmt.Errorf("upload: %w", domain.ErrInvalidFormat),
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error":"Invalid file format","details":"Supported formats: jpeg, png, webp"}`,
		},
		{
			name:       "forbidden",
			err:        fmt.Errorf("delete: %w", domain.ErrForbidden),
			wantStatus: http.StatusForbidden,
			wantBody:   `{"error":"Forbidden","details":"You can only delete your own avatars"}`,
		},
		{
			name:       "not found hides wrapped chain",
			err:        fmt.Errorf("get avatar: %w", domain.ErrNotFound),
			wantStatus: http.StatusNotFound,
			wantBody:   `{"error":"Avatar not found"}`,
		},
		{
			name:       "too large with max_size",
			err:        domain.ErrTooLarge,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantBody:   `{"error":"File too large","max_size":10485760}`,
		},
		{
			name:       "unknown error hides internals",
			err:        errors.New("db is down"),
			wantStatus: http.StatusInternalServerError,
			wantBody:   `{"error":"Internal server error"}`,
		},
	}

	logger := discardLogger()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/1", nil)
			writeServiceError(rec, req, logger, tt.err, maxUploadSize)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Body.String(); got != tt.wantBody+"\n" {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
		})
	}
}
