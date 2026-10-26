package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Логика /health проверяется в health_test.go, здесь — только что маршруты подключены.
func TestRouter(t *testing.T) {
	router := newTestRouter(&fakeAvatarService{})

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
		{"metadata is routed", http.MethodGet, "/api/v1/avatars/x/metadata", http.StatusBadRequest, detailsInvalidAvatarID},
		{"list is routed", http.MethodGet, "/api/v1/users/u/avatars", http.StatusOK, `{"items":[]}`},
		{"delete is routed", http.MethodDelete, "/api/v1/avatars/x", http.StatusBadRequest, detailsUserIDRequired},
		{"delete current is routed", http.MethodDelete, "/api/v1/users/u/avatar", http.StatusBadRequest, detailsUserIDRequired},
		{"root redirects to upload form", http.MethodGet, "/", http.StatusFound, ""},
		{"upload form is routed", http.MethodGet, "/web/upload", http.StatusOK, "<form"},
		{"web upload is routed", http.MethodPost, "/web/upload", http.StatusBadRequest, "<form"},
		{"gallery is routed", http.MethodGet, "/web/gallery/u", http.StatusOK, "No avatars yet"},
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
