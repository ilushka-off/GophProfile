package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORS(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	tests := []struct {
		name        string
		allowed     []string
		method      string
		origin      string
		preflight   bool
		wantStatus  int
		wantOrigin  string
		wantMethods bool
	}{
		{"allowed origin", []string{"https://app.example"}, http.MethodGet, "https://app.example", false, http.StatusOK, "https://app.example", false},
		{"other origin gets no headers", []string{"https://app.example"}, http.MethodGet, "https://evil.example", false, http.StatusOK, "", false},
		{"no origin is a same-origin request", []string{"https://app.example"}, http.MethodGet, "", false, http.StatusOK, "", false},
		{"wildcard", []string{"*"}, http.MethodPost, "https://any.example", false, http.StatusOK, "*", false},
		{"preflight allowed", []string{"https://app.example"}, http.MethodOptions, "https://app.example", true, http.StatusNoContent, "https://app.example", true},
		{"preflight denied", []string{"https://app.example"}, http.MethodOptions, "https://evil.example", true, http.StatusNoContent, "", false},
		{"plain options goes to the handler", []string{"https://app.example"}, http.MethodOptions, "https://app.example", false, http.StatusOK, "https://app.example", false},
		{"disabled", nil, http.MethodGet, "https://app.example", false, http.StatusOK, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/v1/avatars", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.preflight {
				req.Header.Set("Access-Control-Request-Method", http.MethodDelete)
			}
			rec := httptest.NewRecorder()
			CORS(tt.allowed)(next).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tt.wantOrigin {
				t.Errorf("Allow-Origin = %q, want %q", got, tt.wantOrigin)
			}
			gotMethods := rec.Header().Get("Access-Control-Allow-Methods") == "GET, POST, DELETE" &&
				rec.Header().Get("Access-Control-Allow-Headers") == "Content-Type, X-User-ID, If-None-Match"
			if gotMethods != tt.wantMethods {
				t.Errorf("preflight headers = %v, want %v: %v", gotMethods, tt.wantMethods, rec.Header())
			}
			if tt.wantOrigin != "" && !tt.preflight && rec.Header().Get("Access-Control-Expose-Headers") != "ETag, Retry-After" {
				t.Errorf("Expose-Headers = %q", rec.Header().Get("Access-Control-Expose-Headers"))
			}
		})
	}
}
