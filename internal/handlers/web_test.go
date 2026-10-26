package handlers

import (
	"bytes"
	"errors"
	"html/template"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"go-avatar-service/internal/domain"
	"go-avatar-service/web"
)

var testTemplates = template.Must(web.Templates())

func newTestWebHandler(svc *fakeAvatarService) *WebHandler {
	return NewWebHandler(svc, testTemplates, testMaxUploadSize, discardLogger())
}

// newTestRouter — роутер со всеми хендлерами поверх одного фейкового сервиса.
func newTestRouter(svc *fakeAvatarService) http.Handler {
	return NewRouter(discardLogger(), RouterOptions{},
		healthyHandler(),
		NewAvatarHandler(svc, testMaxUploadSize, discardLogger()),
		newTestWebHandler(svc),
	)
}

// webUploadRequest собирает POST /web/upload с полями user_id (если не пустой) и file.
func webUploadRequest(t *testing.T, userID string, content []byte) *http.Request {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if userID != "" {
		if err := mw.WriteField(formFieldUserID, userID); err != nil {
			t.Fatalf("write user_id: %v", err)
		}
	}
	fw, err := mw.CreateFormFile(formFieldFile, "me.png")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/web/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestWebUploadRedirectsToGallery(t *testing.T) {
	svc := &fakeAvatarService{avatar: &domain.Avatar{ID: uuid.New(), UserID: "user 42/x"}}
	rec := httptest.NewRecorder()
	newTestRouter(svc).ServeHTTP(rec, webUploadRequest(t, "user 42/x", []byte("png bytes")))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303, body %s", rec.Code, rec.Body)
	}
	if loc := rec.Header().Get("Location"); loc != "/web/gallery/user%2042%2Fx" {
		t.Errorf("Location = %q, want escaped gallery path", loc)
	}
	if svc.gotInput.UserID != "user 42/x" || string(svc.gotBody) != "png bytes" {
		t.Errorf("service got user %q body %q", svc.gotInput.UserID, svc.gotBody)
	}
}

func TestWebUploadErrorsRenderForm(t *testing.T) {
	tests := []struct {
		name       string
		req        func(t *testing.T) *http.Request
		err        error
		wantStatus int
		wantText   string
	}{
		{
			name:       "no user id",
			req:        func(t *testing.T) *http.Request { return webUploadRequest(t, "", []byte("x")) },
			wantStatus: http.StatusBadRequest,
			wantText:   detailsUserIDRequired,
		},
		{
			name: "not multipart",
			req: func(*testing.T) *http.Request {
				return httptest.NewRequest(http.MethodPost, "/web/upload", strings.NewReader("a=b"))
			},
			wantStatus: http.StatusBadRequest,
			wantText:   detailsNotMultipart,
		},
		{
			name:       "too large",
			req:        func(t *testing.T) *http.Request { return webUploadRequest(t, "user-42", make([]byte, 2<<20)) },
			wantStatus: http.StatusRequestEntityTooLarge,
			wantText:   "File too large: max 1.0 KB",
		},
		{
			name:       "invalid format",
			req:        func(t *testing.T) *http.Request { return webUploadRequest(t, "user-42", []byte("x")) },
			err:        domain.ErrInvalidFormat,
			wantStatus: http.StatusBadRequest,
			wantText:   "Invalid file format: Supported formats: jpeg, png, webp",
		},
		{
			name:       "internal",
			req:        func(t *testing.T) *http.Request { return webUploadRequest(t, "user-42", []byte("x")) },
			err:        errors.New("s3 down"),
			wantStatus: http.StatusInternalServerError,
			wantText:   msgInternal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			newTestRouter(&fakeAvatarService{err: tt.err}).ServeHTTP(rec, tt.req(t))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("Content-Type = %q, want html", ct)
			}
			body := rec.Body.String()
			if !strings.Contains(body, "<form") || !strings.Contains(body, template.HTMLEscapeString(tt.wantText)) {
				t.Errorf("body does not contain the form with %q", tt.wantText)
			}
		})
	}
}

func TestWebUploadFormPrefillsUserID(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestRouter(&fakeAvatarService{}).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/web/upload?user_id="+url.QueryEscape(`<b>"x"</b>`), nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `value="&lt;b&gt;&#34;x&#34;&lt;/b&gt;"`) {
		t.Error("user_id is not prefilled or not escaped")
	}
}

func TestWebGallery(t *testing.T) {
	id := uuid.MustParse("0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10")
	svc := &fakeAvatarService{avatars: []domain.Avatar{{
		ID:               id,
		UserID:           "user-42",
		FileName:         "me.png",
		MimeType:         "image/png",
		SizeBytes:        1536,
		Width:            640,
		Height:           480,
		ProcessingStatus: domain.ProcessingStatusPending,
		CreatedAt:        time.Now(),
	}}}

	rec := httptest.NewRecorder()
	newTestRouter(svc).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/web/gallery/user-42", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.gotUserID != "user-42" {
		t.Errorf("service got user %q, want user-42", svc.gotUserID)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`src="/api/v1/avatars/` + id.String() + `?size=300x300"`,
		"PNG · 640×480 · 1.5 KB",
		"processing",
		`data-delete="` + id.String() + `"`,
		`const userId = "user-42";`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("gallery does not contain %q", want)
		}
	}
}

func TestWebGalleryError(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestRouter(&fakeAvatarService{err: errors.New("db down")}).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/web/gallery/user-42", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestHumanSize(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{10 << 20, "10.0 MB"},
	}
	for _, tt := range tests {
		if got := humanSize(tt.n); got != tt.want {
			t.Errorf("humanSize(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
