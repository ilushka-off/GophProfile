package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Логика /health проверяется в health_test.go, здесь — только что маршруты подключены.
func TestRouter(t *testing.T) {
	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<h1>avatars</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(discardLogger(), NewHealthHandler(fakePinger{}, discardLogger()), webDir)

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantBody   string
	}{
		{"health is routed", http.MethodGet, "/health", http.StatusOK, `"status":"ok"`},
		{"web redirects to slash", http.MethodGet, "/web", http.StatusMovedPermanently, ""},
		{"web serves index", http.MethodGet, "/web/", http.StatusOK, "<h1>avatars</h1>"},
		{"unknown route is json 404", http.MethodGet, "/api/v1/nope", http.StatusNotFound, `"error":"not found"`},
		{"wrong method is json 405", http.MethodPost, "/health", http.StatusMethodNotAllowed, `"error":"method not allowed"`},
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
