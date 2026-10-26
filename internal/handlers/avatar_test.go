package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/services"
)

const testMaxUploadSize = 1 << 10 // 1 KiB, чтобы не гонять в тестах 10 MB

// fakeAvatarService запоминает, что пришло в Upload, и отвечает заданными avatar и err.
type fakeAvatarService struct {
	avatar  *domain.Avatar
	avatars []domain.Avatar
	obj     *domain.Object
	file    *services.File
	err     error
	// openErr — ошибка Open: файл найден в БД, но не открылся.
	openErr error

	called     bool
	opened     bool
	gotInput   services.UploadInput
	gotBody    []byte
	gotID      uuid.UUID
	gotUserID  string
	gotActorID string
	gotOpts    services.FileOptions
}

// closeTracker — тело объекта S3, которое запоминает, что его закрыли.
type closeTracker struct {
	io.Reader
	closed bool
}

func (c *closeTracker) Close() error {
	c.closed = true
	return nil
}

// resolved — что вернут ResolveFile и ResolveCurrentFile: file, если задан,
// иначе неизменяемый файл с ETag "test".
func (f *fakeAvatarService) resolved() (*services.File, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.file != nil {
		return f.file, nil
	}
	return &services.File{ETag: `"test"`, Immutable: true}, nil
}

func (f *fakeAvatarService) ResolveFile(_ context.Context, id uuid.UUID, opts services.FileOptions) (*services.File, error) {
	f.called = true
	f.gotID = id
	f.gotOpts = opts
	return f.resolved()
}

func (f *fakeAvatarService) ResolveCurrentFile(_ context.Context, userID string, opts services.FileOptions) (*services.File, error) {
	f.called = true
	f.gotUserID = userID
	f.gotOpts = opts
	return f.resolved()
}

func (f *fakeAvatarService) Open(context.Context, *services.File) (*domain.Object, error) {
	f.opened = true
	return f.obj, f.openErr
}

func (f *fakeAvatarService) Upload(_ context.Context, in services.UploadInput) (*domain.Avatar, error) {
	f.called = true
	f.gotInput = in
	f.gotBody, _ = io.ReadAll(in.File)
	return f.avatar, f.err
}

// multipartBody собирает тело формы с одним файлом в поле field.
func multipartBody(t *testing.T, field, fileName string, content []byte) (*bytes.Buffer, string) {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile(field, fileName)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

func newUploadRequest(body io.Reader, contentType, userID string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/avatars", body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if userID != "" {
		req.Header.Set(headerUserID, userID)
	}
	return req
}

func TestAvatarHandlerUploadCreated(t *testing.T) {
	id := uuid.MustParse("0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10")
	svc := &fakeAvatarService{avatar: &domain.Avatar{
		ID:               id,
		UserID:           "user-42",
		ProcessingStatus: domain.ProcessingStatusPending,
		CreatedAt:        time.Date(2026, 10, 6, 13, 0, 0, 680919000, time.FixedZone("MSK", 3*60*60)),
	}}
	h := NewAvatarHandler(svc, testMaxUploadSize, discardLogger())

	body, ct := multipartBody(t, formFieldFile, "me.jpg", []byte("image bytes"))
	rec := httptest.NewRecorder()
	h.Upload(rec, newUploadRequest(body, ct, "user-42"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body %s", rec.Code, http.StatusCreated, rec.Body)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	want := map[string]any{
		"id":         id.String(),
		"user_id":    "user-42",
		"url":        "/api/v1/avatars/" + id.String(),
		"status":     "processing",
		"created_at": "2026-10-06T10:00:00Z",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}

	in := svc.gotInput
	if in.UserID != "user-42" || in.FileName != "me.jpg" || in.Size != int64(len("image bytes")) {
		t.Errorf("service input = %+v, want user-42, me.jpg, size %d", in, len("image bytes"))
	}
	if string(svc.gotBody) != "image bytes" {
		t.Errorf("service got file %q, want %q", svc.gotBody, "image bytes")
	}
}

func TestAvatarHandlerUploadErrors(t *testing.T) {
	validBody := func(t *testing.T) (io.Reader, string) {
		return multipartBody(t, formFieldFile, "me.jpg", []byte("image bytes"))
	}

	tests := []struct {
		name        string
		userID      string
		body        func(t *testing.T) (io.Reader, string)
		svcErr      error
		wantStatus  int
		wantBody    string
		wantSvcCall bool
	}{
		{
			name:       "no user id",
			body:       validBody,
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error":"Invalid request","details":"X-User-ID header is required"}`,
		},
		{
			name:       "user id too long",
			userID:     strings.Repeat("я", maxUserIDLength+1),
			body:       validBody,
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error":"Invalid request","details":"X-User-ID must be at most 255 characters"}`,
		},
		{
			name:   "not multipart",
			userID: "user-42",
			body: func(*testing.T) (io.Reader, string) {
				return strings.NewReader(`{"file":"x"}`), "application/json"
			},
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error":"Invalid request","details":"request body must be multipart/form-data"}`,
		},
		{
			name:   "no file field",
			userID: "user-42",
			body: func(t *testing.T) (io.Reader, string) {
				return multipartBody(t, "image", "me.jpg", []byte("image bytes"))
			},
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error":"Invalid request","details":"file is required"}`,
		},
		{
			name:   "body over the limit",
			userID: "user-42",
			body: func(t *testing.T) (io.Reader, string) {
				return multipartBody(t, formFieldFile, "big.jpg", bytes.Repeat([]byte("x"), testMaxUploadSize+multipartOverhead+1))
			},
			wantStatus: http.StatusRequestEntityTooLarge,
			wantBody:   `{"error":"File too large","max_size":1024}`,
		},
		{
			name:        "service says too large",
			userID:      "user-42",
			body:        validBody,
			svcErr:      domain.ErrTooLarge,
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantBody:    `{"error":"File too large","max_size":1024}`,
			wantSvcCall: true,
		},
		{
			name:        "invalid format",
			userID:      "user-42",
			body:        validBody,
			svcErr:      domain.ErrInvalidFormat,
			wantStatus:  http.StatusBadRequest,
			wantBody:    `{"error":"Invalid file format","details":"Supported formats: jpeg, png, webp"}`,
			wantSvcCall: true,
		},
		{
			name:        "internal error",
			userID:      "user-42",
			body:        validBody,
			svcErr:      errors.New("s3 is down"),
			wantStatus:  http.StatusInternalServerError,
			wantBody:    `{"error":"Internal server error"}`,
			wantSvcCall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeAvatarService{err: tt.svcErr}
			h := NewAvatarHandler(svc, testMaxUploadSize, discardLogger())

			body, ct := tt.body(t)
			rec := httptest.NewRecorder()
			h.Upload(rec, newUploadRequest(body, ct, tt.userID))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tt.wantBody {
				t.Errorf("body = %s, want %s", got, tt.wantBody)
			}
			if svc.called != tt.wantSvcCall {
				t.Errorf("service called = %v, want %v", svc.called, tt.wantSvcCall)
			}
		})
	}
}

// getAvatar прогоняет GET через роутер, чтобы {avatar_id} разобрал chi.
func getAvatar(svc *fakeAvatarService, path string) *httptest.ResponseRecorder {
	router := newTestRouter(svc)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestAvatarHandlerGetStreamsFile(t *testing.T) {
	id := uuid.MustParse("0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10")
	body := &closeTracker{Reader: strings.NewReader("png bytes")}
	svc := &fakeAvatarService{obj: &domain.Object{Body: body, Size: int64(len("png bytes")), ContentType: "image/png"}}

	rec := getAvatar(svc, "/api/v1/avatars/"+id.String())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body %s", rec.Code, http.StatusOK, rec.Body)
	}
	if svc.gotID != id {
		t.Errorf("service got id %s, want %s", svc.gotID, id)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "9" {
		t.Errorf("Content-Length = %q, want 9", got)
	}
	if rec.Body.String() != "png bytes" {
		t.Errorf("body = %q, want %q", rec.Body, "png bytes")
	}
	if !body.closed {
		t.Error("object body was not closed")
	}
}

func TestAvatarHandlerGetErrors(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		svcErr      error
		wantStatus  int
		wantBody    string
		wantSvcCall bool
	}{
		{
			name:       "invalid uuid",
			path:       "/api/v1/avatars/not-a-uuid",
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error":"Invalid request","details":"avatar_id must be a valid UUID"}`,
		},
		{
			name:        "not found or deleted",
			path:        "/api/v1/avatars/" + uuid.NewString(),
			svcErr:      domain.ErrNotFound,
			wantStatus:  http.StatusNotFound,
			wantBody:    `{"error":"Avatar not found"}`,
			wantSvcCall: true,
		},
		{
			name:        "row exists but file is missing in s3",
			path:        "/api/v1/avatars/" + uuid.NewString(),
			svcErr:      domain.ErrObjectNotFound,
			wantStatus:  http.StatusInternalServerError,
			wantBody:    `{"error":"Internal server error"}`,
			wantSvcCall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeAvatarService{err: tt.svcErr}
			rec := getAvatar(svc, tt.path)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tt.wantBody {
				t.Errorf("body = %s, want %s", got, tt.wantBody)
			}
			if svc.called != tt.wantSvcCall {
				t.Errorf("service called = %v, want %v", svc.called, tt.wantSvcCall)
			}
		})
	}
}

func TestAvatarHandlerGetFileOptions(t *testing.T) {
	id := uuid.New()
	tests := []struct {
		name  string
		query string
		want  services.FileOptions
	}{
		{"defaults", "", services.FileOptions{Size: domain.SizeOriginal}},
		{"thumbnail", "?size=100x100", services.FileOptions{Size: domain.ThumbnailSize100}},
		{"explicit original", "?size=original", services.FileOptions{Size: domain.SizeOriginal}},
		{"format", "?format=webp", services.FileOptions{Size: domain.SizeOriginal, Format: "image/webp"}},
		{"both", "?size=300x300&format=png", services.FileOptions{Size: domain.ThumbnailSize300, Format: "image/png"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeAvatarService{obj: &domain.Object{Body: io.NopCloser(strings.NewReader("x")), Size: 1, ContentType: "image/png"}}

			rec := getAvatar(svc, "/api/v1/avatars/"+id.String()+tt.query)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body %s", rec.Code, http.StatusOK, rec.Body)
			}
			if svc.gotOpts != tt.want {
				t.Errorf("options = %+v, want %+v", svc.gotOpts, tt.want)
			}
		})
	}
}

func TestAvatarHandlerGetInvalidQuery(t *testing.T) {
	sizeErr := `{"error":"Invalid request","details":"size: allowed values 100x100, 300x300, original"}`
	formatErr := `{"error":"Invalid request","details":"format: allowed values jpeg, png, webp"}`
	tests := []struct {
		name     string
		path     string
		wantBody string
	}{
		{"unknown size", "/api/v1/avatars/" + uuid.NewString() + "?size=50x50", sizeErr},
		{"empty size", "/api/v1/avatars/" + uuid.NewString() + "?size=", sizeErr},
		{"unknown format", "/api/v1/avatars/" + uuid.NewString() + "?format=gif", formatErr},
		{"format as mime type", "/api/v1/avatars/" + uuid.NewString() + "?format=image/png", formatErr},
		{"current avatar, unknown size", "/api/v1/users/user-42/avatar?size=big", sizeErr},
		{
			"uuid is checked before query",
			"/api/v1/avatars/not-a-uuid?size=big",
			`{"error":"Invalid request","details":"avatar_id must be a valid UUID"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeAvatarService{}
			rec := getAvatar(svc, tt.path)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tt.wantBody {
				t.Errorf("body = %s, want %s", got, tt.wantBody)
			}
			if svc.called {
				t.Error("service was called")
			}
		})
	}
}

func TestAvatarHandlerGetConvertedHasNoContentLength(t *testing.T) {
	svc := &fakeAvatarService{obj: &domain.Object{Body: io.NopCloser(strings.NewReader("webp bytes")), Size: -1, ContentType: "image/webp"}}

	rec := getAvatar(svc, "/api/v1/avatars/"+uuid.NewString()+"?format=webp")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Errorf("Content-Length = %q, want none", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/webp" {
		t.Errorf("Content-Type = %q, want image/webp", got)
	}
	if rec.Body.String() != "webp bytes" {
		t.Errorf("body = %q, want %q", rec.Body, "webp bytes")
	}
}

func TestAvatarHandlerGetCurrent(t *testing.T) {
	body := &closeTracker{Reader: strings.NewReader("jpeg bytes")}
	svc := &fakeAvatarService{obj: &domain.Object{Body: body, Size: 10, ContentType: "image/jpeg"}}

	rec := getAvatar(svc, "/api/v1/users/user-42/avatar?size=300x300")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body %s", rec.Code, http.StatusOK, rec.Body)
	}
	if svc.gotUserID != "user-42" {
		t.Errorf("service got user %q, want user-42", svc.gotUserID)
	}
	if want := (services.FileOptions{Size: domain.ThumbnailSize300}); svc.gotOpts != want {
		t.Errorf("options = %+v, want %+v", svc.gotOpts, want)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", got)
	}
	if rec.Body.String() != "jpeg bytes" {
		t.Errorf("body = %q, want %q", rec.Body, "jpeg bytes")
	}
	if !body.closed {
		t.Error("object body was not closed")
	}
}

func TestAvatarHandlerGetCurrentNotFound(t *testing.T) {
	svc := &fakeAvatarService{err: domain.ErrNotFound}

	rec := getAvatar(svc, "/api/v1/users/nobody/avatar")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"Avatar not found"}` {
		t.Errorf("body = %s", got)
	}
}

// getAvatarWith — как getAvatar, но с заголовками запроса.
func getAvatarWith(svc *fakeAvatarService, path string, header http.Header) *httptest.ResponseRecorder {
	router := newTestRouter(svc)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header = header
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestAvatarHandlerGetCacheHeaders(t *testing.T) {
	tests := []struct {
		name      string
		immutable bool
		want      string
	}{
		{"immutable file is cached for a day", true, "max-age=86400"},
		{"mutable file is revalidated", false, "no-cache"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeAvatarService{
				file: &services.File{ETag: `"abc"`, Immutable: tt.immutable},
				obj:  &domain.Object{Body: io.NopCloser(strings.NewReader("x")), Size: 1, ContentType: "image/png"},
			}

			rec := getAvatar(svc, "/api/v1/avatars/"+uuid.NewString())

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if got := rec.Header().Get("ETag"); got != `"abc"` {
				t.Errorf("ETag = %s, want \"abc\"", got)
			}
			if got := rec.Header().Get("Cache-Control"); got != tt.want {
				t.Errorf("Cache-Control = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAvatarHandlerGetNotModified(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		etag        string
		ifNoneMatch []string
		want304     bool
	}{
		{"same etag", "/api/v1/avatars/" + uuid.NewString(), `"abc"`, []string{`"abc"`}, true},
		{"current avatar, same etag", "/api/v1/users/user-42/avatar", `"abc"`, []string{`"abc"`}, true},
		{"one of the list", "/api/v1/avatars/" + uuid.NewString(), `"abc"`, []string{`"x", "abc"`}, true},
		{"one of several headers", "/api/v1/avatars/" + uuid.NewString(), `"abc"`, []string{`"x"`, `"abc"`}, true},
		{"star", "/api/v1/avatars/" + uuid.NewString(), `"abc"`, []string{"*"}, true},
		{"weak comparison", "/api/v1/avatars/" + uuid.NewString(), `W/"abc"`, []string{`"abc"`}, true},
		{"weak in request", "/api/v1/avatars/" + uuid.NewString(), `"abc"`, []string{`W/"abc"`}, true},
		{"other etag", "/api/v1/avatars/" + uuid.NewString(), `"abc"`, []string{`"abd"`}, false},
		{"no header", "/api/v1/avatars/" + uuid.NewString(), `"abc"`, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeAvatarService{
				file: &services.File{ETag: tt.etag, Immutable: true},
				obj:  &domain.Object{Body: io.NopCloser(strings.NewReader("x")), Size: 1, ContentType: "image/png"},
			}
			header := http.Header{}
			for _, v := range tt.ifNoneMatch {
				header.Add("If-None-Match", v)
			}

			rec := getAvatarWith(svc, tt.path, header)

			if !tt.want304 {
				if rec.Code != http.StatusOK || !svc.opened {
					t.Errorf("status = %d, opened = %v; want 200 and file opened", rec.Code, svc.opened)
				}
				return
			}
			if rec.Code != http.StatusNotModified {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotModified)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("body = %q, want empty", rec.Body)
			}
			if svc.opened {
				t.Error("file was opened for 304")
			}
			if rec.Header().Get("ETag") != tt.etag || rec.Header().Get("Cache-Control") != "max-age=86400" {
				t.Errorf("304 headers: ETag %q, Cache-Control %q", rec.Header().Get("ETag"), rec.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestAvatarHandlerGetErrorIsNotCached(t *testing.T) {
	tests := []struct {
		name string
		svc  *fakeAvatarService
	}{
		{"not found", &fakeAvatarService{err: domain.ErrNotFound}},
		{"file missing in s3", &fakeAvatarService{openErr: domain.ErrObjectNotFound}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := getAvatar(tt.svc, "/api/v1/avatars/"+uuid.NewString())

			if rec.Code < 400 {
				t.Fatalf("status = %d, want an error", rec.Code)
			}
			if got := rec.Header().Get("Cache-Control"); got != "" {
				t.Errorf("Cache-Control = %q on error", got)
			}
			if got := rec.Header().Get("ETag"); got != "" {
				t.Errorf("ETag = %q on error", got)
			}
		})
	}
}

func TestValidateUserID(t *testing.T) {
	tests := []struct {
		userID string
		want   string
	}{
		{"user-42", ""},
		{"Ivan.Petrov_1@example.com", ""},
		{strings.Repeat("a", maxUserIDLength), ""},
		{"", detailsUserIDRequired},
		{strings.Repeat("a", maxUserIDLength+1), detailsUserIDTooLong},
		{"user 42", detailsUserIDInvalidChars},
		{"../admin", detailsUserIDInvalidChars},
		{"user\n42", detailsUserIDInvalidChars},
		{"<script>", detailsUserIDInvalidChars},
		{"пользователь", detailsUserIDInvalidChars},
	}
	for _, tt := range tests {
		if got := validateUserID(tt.userID); got != tt.want {
			t.Errorf("validateUserID(%q) = %q, want %q", tt.userID, got, tt.want)
		}
	}
}
