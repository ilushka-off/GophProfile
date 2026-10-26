package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"go-avatar-service/internal/domain"
)

func (f *fakeAvatarService) ListByUser(_ context.Context, userID string) ([]domain.Avatar, error) {
	f.called = true
	f.gotUserID = userID
	return f.avatars, f.err
}

func listAvatars(svc *fakeAvatarService, userID string) *httptest.ResponseRecorder {
	h := NewAvatarHandler(svc, testMaxUploadSize, discardLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/"+userID+"/avatars", nil)
	return serveRoute(http.MethodGet, "/api/v1/users/{user_id}/avatars", h.List, req)
}

func TestAvatarHandlerList(t *testing.T) {
	newer := testAvatar(uuid.MustParse("0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10"))
	older := testAvatar(uuid.MustParse("1c7d7d0f-6a1f-4b46-8b54-8a4c2d3e0f21"))
	older.ThumbnailS3Keys = nil
	older.ProcessingStatus = domain.ProcessingStatusFailed

	svc := &fakeAvatarService{avatars: []domain.Avatar{*newer, *older}}
	rec := listAvatars(svc, "user-42")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body %s", rec.Code, rec.Body)
	}
	if svc.gotUserID != "user-42" {
		t.Errorf("service got user %q, want user-42", svc.gotUserID)
	}

	var got struct {
		Items []struct {
			ID         string `json:"id"`
			Status     string `json:"status"`
			Thumbnails []any  `json:"thumbnails"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(got.Items))
	}
	if got.Items[0].ID != newer.ID.String() || got.Items[1].ID != older.ID.String() {
		t.Errorf("order = [%s %s], want service order", got.Items[0].ID, got.Items[1].ID)
	}
	if got.Items[0].Status != "ready" || len(got.Items[0].Thumbnails) != 2 {
		t.Errorf("first item = %+v, want ready with 2 thumbnails", got.Items[0])
	}
	if got.Items[1].Status != "failed" || got.Items[1].Thumbnails == nil {
		t.Errorf("second item = %+v, want failed with empty thumbnails", got.Items[1])
	}
}

func TestAvatarHandlerListEmpty(t *testing.T) {
	rec := listAvatars(&fakeAvatarService{avatars: []domain.Avatar{}}, "nobody")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != `{"items":[]}`+"\n" {
		t.Errorf("body = %q, want {\"items\":[]}", got)
	}
}

func TestAvatarHandlerListError(t *testing.T) {
	rec := listAvatars(&fakeAvatarService{err: errors.New("db down")}, "user-42")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
