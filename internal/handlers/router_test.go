package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Логика /health проверяется в health_test.go, здесь — только что маршруты подключены.
func TestRouter(t *testing.T) {
	router := NewRouter(discardLogger(),
		healthyHandler(),
		NewAvatarHandler(&fakeAvatarService{}, testMaxUploadSize, discardLogger()),
	)

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantBody   string
	}{
		{"health is routed", http.MethodGet, "/health", http.StatusOK, `"status":"ok"`},
		{"unknown route is json 404", http.MethodGet, "/api/v1/nope", http.StatusNotFound, `{"error":"Not found"}`},
		{"wrong method is json 405", http.MethodPost, "/health", http.StatusMethodNotAllowed, `{"error":"Method not allowed"}`},
		{"upload is routed", http.MethodPost, "/api/v1/avatars", http.StatusBadRequest, detailsUserIDRequired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want it to contain %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}
