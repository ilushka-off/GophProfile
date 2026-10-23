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
	avatar *domain.Avatar
	err    error

	called   bool
	gotInput services.UploadInput
	gotBody  []byte
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
		CreatedAt:        time.Date(2026, 10, 6, 13, 0, 0, 0, time.FixedZone("MSK", 3*60*60)),
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
