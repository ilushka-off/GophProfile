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

// Запрос с заявленным телом больше лимита отклоняется middleware: сервис не вызывается.
func TestRouterRejectsLargeBodies(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		header   string
		wantBody string
	}{
		{"api answers json", "/api/v1/avatars", "user-42", `{"error":"File too large","max_size":1024}`},
		{"web answers the form", "/web/upload", "", "File too large: max 1.0 KB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeAvatarService{}
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader("x"))
			req.ContentLength = testMaxUploadSize + multipartOverhead + 1
			req.Header.Set(headerUserID, tt.header)

			rec := httptest.NewRecorder()
			newTestRouter(svc).ServeHTTP(rec, req)

			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("status = %d, want 413", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want it to contain %q", rec.Body.String(), tt.wantBody)
			}
			if svc.called {
				t.Error("service was called for an oversized request")
			}
		})
	}
}

func TestRouterRateLimit(t *testing.T) {
	router := NewRouter(discardLogger(), RouterOptions{RateLimitRPS: 0.1, RateLimitBurst: 1},
		healthyHandler(),
		NewAvatarHandler(&fakeAvatarService{}, testMaxUploadSize, discardLogger()),
		newTestWebHandler(&fakeAvatarService{}),
	)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	if rec := get("/api/v1/users/u/avatars"); rec.Code != http.StatusOK {
		t.Fatalf("first request: %d", rec.Code)
	}
	rec := get("/web/upload") // веб и API делят одно ведро
	if rec.Code != http.StatusTooManyRequests || rec.Body.String() != `{"error":"Too many requests"}`+"\n" {
		t.Errorf("limited request: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After header")
	}
	for range 3 {
		if rec := get("/health"); rec.Code != http.StatusOK {
			t.Errorf("/health must not be limited, got %d", rec.Code)
		}
	}
}

// Preflight перехватывается до роутинга: маршрутов OPTIONS в роутере нет.
func TestRouterCORSPreflight(t *testing.T) {
	router := NewRouter(discardLogger(), RouterOptions{CORSAllowedOrigins: []string{"https://app.example"}},
		healthyHandler(),
		NewAvatarHandler(&fakeAvatarService{}, testMaxUploadSize, discardLogger()),
		newTestWebHandler(&fakeAvatarService{}),
	)

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/avatars/0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10", nil)
	req.Header.Set("Origin", "https://app.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodDelete)
	req.Header.Set("Access-Control-Request-Headers", "x-user-id")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Errorf("Allow-Origin = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "X-User-ID") {
		t.Errorf("Allow-Headers = %q, want X-User-ID", got)
	}
}
