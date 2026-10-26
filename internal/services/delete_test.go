package services

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/domain"
)

func (f *fakeRepo) SoftDelete(_ context.Context, id uuid.UUID) error {
	*f.log = append(*f.log, "repo.SoftDelete")
	f.softDeleted = id
	return f.softDeleteErr
}

func (f *fakeRepo) Restore(_ context.Context, id uuid.UUID) error {
	*f.log = append(*f.log, "repo.Restore")
	f.restored = id
	return f.restoreErr
}

func (f *fakeRepo) Delete(_ context.Context, id uuid.UUID) error {
	*f.log = append(*f.log, "repo.Delete")
	f.deleted = id
	return f.deleteErr
}

func (f *fakeRepo) ListByUser(_ context.Context, userID string) ([]domain.Avatar, error) {
	*f.log = append(*f.log, "repo.ListByUser")
	f.gotUserID = userID
	return f.list, f.err
}

var testAvatarID = uuid.MustParse("0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10")

func ownedAvatar(userID string) *domain.Avatar {
	return &domain.Avatar{
		ID:     testAvatarID,
		UserID: userID,
		S3Key:  domain.OriginalKey(testAvatarID, "png"),
	}
}

func TestAvatarServiceGetAndList(t *testing.T) {
	d := newDeps()
	d.repo.byID = ownedAvatar("user-42")
	d.repo.list = []domain.Avatar{*ownedAvatar("user-42")}

	got, err := d.svc.Get(context.Background(), testAvatarID)
	if err != nil || got != d.repo.byID || d.repo.gotID != testAvatarID {
		t.Errorf("Get = %v, %v; want repo avatar for %s", got, err, testAvatarID)
	}

	list, err := d.svc.ListByUser(context.Background(), "user-42")
	if err != nil || len(list) != 1 || d.repo.gotUserID != "user-42" {
		t.Errorf("ListByUser = %v, %v; want one avatar of user-42", list, err)
	}
}

// checkDeleteEvent проверяет, что опубликовано одно avatar.deleted с ключами
// оригинала и всех миниатюр.
func checkDeleteEvent(t *testing.T, msgs []broker.Message) {
	t.Helper()

	if len(msgs) != 1 {
		t.Fatalf("published %d messages, want 1", len(msgs))
	}
	msg := msgs[0]
	if msg.Type != broker.TypeAvatarDeleted || msg.Key != testAvatarID.String() {
		t.Errorf("message type/key = %s/%s, want %s/%s", msg.Type, msg.Key, broker.TypeAvatarDeleted, testAvatarID)
	}
	if _, err := uuid.Parse(msg.ID); err != nil {
		t.Errorf("message id %q is not a uuid", msg.ID)
	}

	var ev broker.AvatarDeleteEvent
	if err := json.Unmarshal(msg.Body, &ev); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	want := []string{
		"originals/0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10.png",
		"thumbnails/0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10/100x100.jpg",
		"thumbnails/0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10/300x300.jpg",
	}
	if ev.AvatarID != testAvatarID.String() || !slices.Equal(ev.S3Keys, want) {
		t.Errorf("event = %+v, want avatar %s with keys %v", ev, testAvatarID, want)
	}
}

func TestAvatarServiceDelete(t *testing.T) {
	d := newDeps()
	d.repo.byID = ownedAvatar("user-42")

	if err := d.svc.Delete(context.Background(), "user-42", testAvatarID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if want := (calls{"repo.GetByID", "repo.SoftDelete", "publisher.Publish"}); !slices.Equal(*d.log, want) {
		t.Errorf("calls = %v, want %v", *d.log, want)
	}
	if d.repo.softDeleted != testAvatarID {
		t.Errorf("soft deleted %s, want %s", d.repo.softDeleted, testAvatarID)
	}
	checkDeleteEvent(t, d.publisher.msgs)
}

func TestAvatarServiceDeleteCurrent(t *testing.T) {
	d := newDeps()
	d.repo.byID = ownedAvatar("user-42")

	if err := d.svc.DeleteCurrent(context.Background(), "user-42", "user-42"); err != nil {
		t.Fatalf("DeleteCurrent: %v", err)
	}
	if want := (calls{"repo.GetCurrentByUser", "repo.SoftDelete", "publisher.Publish"}); !slices.Equal(*d.log, want) {
		t.Errorf("calls = %v, want %v", *d.log, want)
	}
	if d.repo.gotUserID != "user-42" || d.repo.softDeleted != testAvatarID {
		t.Errorf("repo got user %q, deleted %s", d.repo.gotUserID, d.repo.softDeleted)
	}
	checkDeleteEvent(t, d.publisher.msgs)
}

func TestAvatarServiceDeleteCurrentFailures(t *testing.T) {
	t.Run("other user", func(t *testing.T) {
		d := newDeps()
		if err := d.svc.DeleteCurrent(context.Background(), "user-7", "user-42"); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("err = %v, want ErrForbidden", err)
		}
		if len(*d.log) != 0 {
			t.Errorf("calls = %v, want none: foreign user must not touch the db", *d.log)
		}
	})
	t.Run("no avatars", func(t *testing.T) {
		d := newDeps()
		d.repo.err = domain.ErrNotFound
		if err := d.svc.DeleteCurrent(context.Background(), "user-42", "user-42"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestAvatarServiceDeleteFailures(t *testing.T) {
	errDB := errors.New("db down")

	tests := []struct {
		name      string
		setup     func(d *deps)
		wantErr   error
		wantCalls calls
	}{
		{
			name:      "not found",
			setup:     func(d *deps) { d.repo.err = domain.ErrNotFound },
			wantErr:   domain.ErrNotFound,
			wantCalls: calls{"repo.GetByID"},
		},
		{
			name:      "someone else's avatar",
			setup:     func(d *deps) { d.repo.byID = ownedAvatar("user-7") },
			wantErr:   domain.ErrForbidden,
			wantCalls: calls{"repo.GetByID"},
		},
		{
			name: "deleted concurrently",
			setup: func(d *deps) {
				d.repo.byID = ownedAvatar("user-42")
				d.repo.softDeleteErr = domain.ErrNotFound
			},
			wantErr:   domain.ErrNotFound,
			wantCalls: calls{"repo.GetByID", "repo.SoftDelete"},
		},
		{
			name: "publish fails, soft delete is rolled back",
			setup: func(d *deps) {
				d.repo.byID = ownedAvatar("user-42")
				d.publisher.err = errDB
			},
			wantErr:   errDB,
			wantCalls: calls{"repo.GetByID", "repo.SoftDelete", "publisher.Publish", "repo.Restore"},
		},
		{
			name: "publish and rollback fail",
			setup: func(d *deps) {
				d.repo.byID = ownedAvatar("user-42")
				d.publisher.err = errDB
				d.repo.restoreErr = errors.New("restore failed")
			},
			wantErr:   errDB,
			wantCalls: calls{"repo.GetByID", "repo.SoftDelete", "publisher.Publish", "repo.Restore"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDeps()
			tt.setup(d)

			err := d.svc.Delete(context.Background(), "user-42", testAvatarID)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if !slices.Equal(*d.log, tt.wantCalls) {
				t.Errorf("calls = %v, want %v", *d.log, tt.wantCalls)
			}
		})
	}
}
