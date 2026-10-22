package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/httpx"
)

func TestWriteServiceError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantError  string
	}{
		{"validation", &domain.ValidationError{Details: "bad size"}, http.StatusBadRequest, msgValidation},
		{"not found", fmt.Errorf("get: %w", domain.ErrNotFound), http.StatusNotFound, msgNotFound},
		{"forbidden", domain.ErrForbidden, http.StatusForbidden, msgForbidden},
		{"invalid format", domain.ErrInvalidFormat, http.StatusUnsupportedMediaType, msgInvalidFormat},
		{"too large", domain.ErrTooLarge, http.StatusRequestEntityTooLarge, msgTooLarge},
		{"unknown", errors.New("db is down"), http.StatusInternalServerError, msgInternal},
	}

	logger := discardLogger()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/1", nil)
			writeServiceError(rec, req, logger, tt.err)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			var body httpx.ErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body %q: %v", rec.Body.String(), err)
			}
			if body.Error != tt.wantError {
				t.Errorf("error = %q, want %q", body.Error, tt.wantError)
			}
			// Внутренние детали 500 не должны утекать клиенту.
			if tt.wantStatus == http.StatusInternalServerError && body.Details != "" {
				t.Errorf("details = %q, want empty for 500", body.Details)
			}
		})
	}
}
