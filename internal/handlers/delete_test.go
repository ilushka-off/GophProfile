package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"go-avatar-service/internal/domain"
)

func (f *fakeAvatarService) Delete(_ context.Context, userID string, id uuid.UUID) error {
	f.called = true
	f.gotUserID = userID
	f.gotID = id
	return f.err
}

func (f *fakeAvatarService) DeleteCurrent(_ context.Context, actorID, userID string) error {
	f.called = true
	f.gotActorID = actorID
	f.gotUserID = userID
	return f.err
}

func TestAvatarHandlerDeleteCurrent(t *testing.T) {
	tests := []struct {
		name       string
		userID     string
		err        error
		wantStatus int
		wantCalled bool
	}{
		{"deleted", "user-42", nil, http.StatusNoContent, true},
		{"no user id", "", nil, http.StatusBadRequest, false},
		{"other user", "user-7", domain.ErrForbidden, http.StatusForbidden, true},
		{"no avatars", "user-42", domain.ErrNotFound, http.StatusNotFound, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeAvatarService{err: tt.err}
			h := NewAvatarHandler(svc, testMaxUploadSize, discardLogger())
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/users/user-42/avatar", nil)
			if tt.userID != "" {
				req.Header.Set(headerUserID, tt.userID)
			}

			rec := serveRoute(http.MethodDelete, "/api/v1/users/{user_id}/avatar", h.DeleteCurrent, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d, body %s", rec.Code, tt.wantStatus, rec.Body)
			}
			if svc.called != tt.wantCalled {
				t.Fatalf("service called = %v, want %v", svc.called, tt.wantCalled)
			}
			if tt.wantCalled && (svc.gotActorID != tt.userID || svc.gotUserID != "user-42") {
				t.Errorf("service got (%q, %q), want (%q, user-42)", svc.gotActorID, svc.gotUserID, tt.userID)
			}
		})
	}
}

func deleteAvatar(svc *fakeAvatarService, path, userID string) *httptest.ResponseRecorder {
	h := NewAvatarHandler(svc, testMaxUploadSize, discardLogger())
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	if userID != "" {
		req.Header.Set(headerUserID, userID)
	}
	return serveRoute(http.MethodDelete, "/api/v1/avatars/{avatar_id}", h.Delete, req)
}

func TestAvatarHandlerDelete(t *testing.T) {
	id := uuid.MustParse("0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10")
	svc := &fakeAvatarService{}

	rec := deleteAvatar(svc, "/api/v1/avatars/"+id.String(), "user-42")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204, body %s", rec.Code, rec.Body)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body)
	}
	if svc.gotUserID != "user-42" || svc.gotID != id {
		t.Errorf("service got (%q, %s), want (user-42, %s)", svc.gotUserID, svc.gotID, id)
	}
}

func TestAvatarHandlerDeleteErrors(t *testing.T) {
	const validPath = "/api/v1/avatars/0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10"

	tests := []struct {
		name       string
		path       string
		userID     string
		err        error
		wantStatus int
		wantBody   string
		wantCalled bool
	}{
		// X-User-ID проверяется раньше avatar_id.
		{"no user id", "/api/v1/avatars/not-a-uuid", "", nil, http.StatusBadRequest, detailsUserIDRequired, false},
		{"invalid uuid", "/api/v1/avatars/not-a-uuid", "user-42", nil, http.StatusBadRequest, detailsInvalidAvatarID, false},
		{"someone else's avatar", validPath, "user-7", domain.ErrForbidden, http.StatusForbidden, detailsForbidden, true},
		{"not found", validPath, "user-42", domain.ErrNotFound, http.StatusNotFound, msgNotFound, true},
		{"internal", validPath, "user-42", errors.New("publish failed"), http.StatusInternalServerError, msgInternal, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeAvatarService{err: tt.err}
			rec := deleteAvatar(svc, tt.path, tt.userID)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want it to contain %q", rec.Body, tt.wantBody)
			}
			if svc.called != tt.wantCalled {
				t.Errorf("service called = %v, want %v", svc.called, tt.wantCalled)
			}
		})
	}
}
