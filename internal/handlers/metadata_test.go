package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"go-avatar-service/internal/domain"
)

func (f *fakeAvatarService) Get(_ context.Context, id uuid.UUID) (*domain.Avatar, error) {
	f.called = true
	f.gotID = id
	return f.avatar, f.err
}

func testAvatar(id uuid.UUID) *domain.Avatar {
	return &domain.Avatar{
		ID:               id,
		UserID:           "user-42",
		FileName:         "me.jpg",
		MimeType:         "image/jpeg",
		SizeBytes:        1024000,
		Width:            1920,
		Height:           1080,
		ProcessingStatus: domain.ProcessingStatusCompleted,
		ThumbnailS3Keys: map[string]string{
			domain.ThumbnailSize300: "thumbnails/x/300x300.jpg",
			domain.ThumbnailSize100: "thumbnails/x/100x100.jpg",
		},
		CreatedAt: time.Date(2026, 10, 6, 13, 0, 0, 500, time.FixedZone("MSK", 3*60*60)),
		UpdatedAt: time.Date(2026, 10, 6, 10, 0, 3, 0, time.UTC),
	}
}

// serveRoute прогоняет запрос через chi с одним маршрутом, чтобы заполнились URL-параметры.
func serveRoute(method, pattern string, h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Method(method, pattern, h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestAvatarHandlerMetadata(t *testing.T) {
	id := uuid.MustParse("0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10")

	tests := []struct {
		name     string
		avatar   *domain.Avatar
		wantBody string
	}{
		{
			name:   "ready with thumbnails in size order",
			avatar: testAvatar(id),
			wantBody: `{"id":"0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10","user_id":"user-42","file_name":"me.jpg",` +
				`"mime_type":"image/jpeg","size":1024000,"dimensions":{"width":1920,"height":1080},` +
				`"thumbnails":[{"size":"100x100","url":"/api/v1/avatars/0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10?size=100x100"},` +
				`{"size":"300x300","url":"/api/v1/avatars/0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10?size=300x300"}],` +
				`"status":"ready","created_at":"2026-10-06T10:00:00Z","updated_at":"2026-10-06T10:00:03Z"}`,
		},
		{
			name: "processing has empty thumbnails array",
			avatar: func() *domain.Avatar {
				a := testAvatar(id)
				a.ThumbnailS3Keys = nil
				a.ProcessingStatus = domain.ProcessingStatusPending
				return a
			}(),
			wantBody: `{"id":"0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10","user_id":"user-42","file_name":"me.jpg",` +
				`"mime_type":"image/jpeg","size":1024000,"dimensions":{"width":1920,"height":1080},` +
				`"thumbnails":[],"status":"processing","created_at":"2026-10-06T10:00:00Z","updated_at":"2026-10-06T10:00:03Z"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeAvatarService{avatar: tt.avatar}
			h := NewAvatarHandler(svc, testMaxUploadSize, discardLogger())

			req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+id.String()+"/metadata", nil)
			rec := serveRoute(http.MethodGet, "/api/v1/avatars/{avatar_id}/metadata", h.Metadata, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200, body %s", rec.Code, rec.Body)
			}
			if svc.gotID != id {
				t.Errorf("service got id %s, want %s", svc.gotID, id)
			}
			if got := rec.Body.String(); got != tt.wantBody+"\n" {
				t.Errorf("body =\n%s\nwant\n%s", got, tt.wantBody)
			}
		})
	}
}

func TestAvatarHandlerMetadataErrors(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		err        error
		wantStatus int
		wantCalled bool
	}{
		{"invalid uuid", "/api/v1/avatars/not-a-uuid/metadata", nil, http.StatusBadRequest, false},
		{"not found", "/api/v1/avatars/0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10/metadata", domain.ErrNotFound, http.StatusNotFound, true},
		{"internal", "/api/v1/avatars/0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10/metadata", context.DeadlineExceeded, http.StatusInternalServerError, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeAvatarService{err: tt.err}
			h := NewAvatarHandler(svc, testMaxUploadSize, discardLogger())

			rec := serveRoute(http.MethodGet, "/api/v1/avatars/{avatar_id}/metadata", h.Metadata,
				httptest.NewRequest(http.MethodGet, tt.path, nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if svc.called != tt.wantCalled {
				t.Errorf("service called = %v, want %v", svc.called, tt.wantCalled)
			}
		})
	}
}
