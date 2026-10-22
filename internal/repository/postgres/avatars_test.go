package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/services"
)

var _ services.AvatarRepository = (*AvatarRepository)(nil)

func newTestAvatar(userID string) *domain.Avatar {
	return &domain.Avatar{
		UserID:           userID,
		FileName:         "me.jpg",
		MimeType:         "image/jpeg",
		SizeBytes:        1024000,
		Width:            1920,
		Height:           1080,
		S3Key:            "originals/me.jpg",
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusPending,
	}
}

// createAvatarAt создаёт аватарку и выставляет ей created_at, чтобы порядок
// в тестах не зависел от того, насколько быстро прошли вставки.
func createAvatarAt(t *testing.T, pool *pgxpool.Pool, repo *AvatarRepository, userID string, createdAt time.Time) *domain.Avatar {
	t.Helper()

	a := newTestAvatar(userID)
	if err := repo.Create(context.Background(), a); err != nil {
		t.Fatalf("create avatar: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE avatars SET created_at = $2 WHERE id = $1`, a.ID, createdAt); err != nil {
		t.Fatalf("set created_at: %v", err)
	}
	a.CreatedAt = createdAt
	return a
}

func TestAvatarRepositoryCreateAndGet(t *testing.T) {
	pool := testPool(t)
	repo := NewAvatarRepository(pool)
	ctx := context.Background()

	a := newTestAvatar("user-42")
	a.ThumbnailS3Keys = map[string]string{"100x100": "thumbnails/x/100x100.jpg"}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("create: %v", err)
	}
	if a.ID == uuid.Nil {
		t.Fatal("create did not set id")
	}
	if a.CreatedAt.IsZero() || a.UpdatedAt.IsZero() {
		t.Fatal("create did not set created_at/updated_at")
	}

	got, err := repo.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if got.UserID != a.UserID || got.FileName != a.FileName || got.MimeType != a.MimeType ||
		got.SizeBytes != a.SizeBytes || got.Width != a.Width || got.Height != a.Height || got.S3Key != a.S3Key ||
		got.UploadStatus != a.UploadStatus || got.ProcessingStatus != a.ProcessingStatus {
		t.Errorf("got %+v, want fields of %+v", got, a)
	}
	if got.ThumbnailS3Keys["100x100"] != "thumbnails/x/100x100.jpg" || len(got.ThumbnailS3Keys) != 1 {
		t.Errorf("thumbnail keys = %v, want the ones that were stored", got.ThumbnailS3Keys)
	}
	if !got.CreatedAt.Equal(a.CreatedAt) {
		t.Errorf("created_at = %v, want %v", got.CreatedAt, a.CreatedAt)
	}
	if got.DeletedAt != nil {
		t.Errorf("deleted_at = %v, want nil", got.DeletedAt)
	}
}

func TestAvatarRepositoryGetByIDNotFound(t *testing.T) {
	repo := NewAvatarRepository(testPool(t))

	_, err := repo.GetByID(context.Background(), uuid.New())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want domain.ErrNotFound", err)
	}
}

func TestAvatarRepositorySoftDelete(t *testing.T) {
	pool := testPool(t)
	repo := NewAvatarRepository(pool)
	ctx := context.Background()

	a := newTestAvatar("user-42")
	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := repo.SoftDelete(ctx, a.ID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if _, err := repo.GetByID(ctx, a.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get after delete: err = %v, want domain.ErrNotFound", err)
	}

	// Удаление мягкое: строка осталась, у неё стоит deleted_at.
	var deleted bool
	if err := pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM avatars WHERE id = $1`, a.ID).Scan(&deleted); err != nil {
		t.Fatalf("select deleted row: %v", err)
	}
	if !deleted {
		t.Error("row has no deleted_at after soft delete")
	}

	if err := repo.SoftDelete(ctx, a.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second delete: err = %v, want domain.ErrNotFound", err)
	}
	if err := repo.SoftDelete(ctx, uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("delete missing: err = %v, want domain.ErrNotFound", err)
	}
}

func TestAvatarRepositoryCurrentAndList(t *testing.T) {
	pool := testPool(t)
	repo := NewAvatarRepository(pool)
	ctx := context.Background()

	base := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	oldest := createAvatarAt(t, pool, repo, "user-42", base)
	middle := createAvatarAt(t, pool, repo, "user-42", base.Add(time.Minute))
	newest := createAvatarAt(t, pool, repo, "user-42", base.Add(2*time.Minute))
	createAvatarAt(t, pool, repo, "user-7", base.Add(3*time.Minute))

	current, err := repo.GetCurrentByUser(ctx, "user-42")
	if err != nil {
		t.Fatalf("get current: %v", err)
	}
	if current.ID != newest.ID {
		t.Errorf("current = %s, want newest %s", current.ID, newest.ID)
	}

	list, err := repo.ListByUser(ctx, "user-42")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	wantIDs := []uuid.UUID{newest.ID, middle.ID, oldest.ID}
	if len(list) != len(wantIDs) {
		t.Fatalf("list has %d items, want %d", len(list), len(wantIDs))
	}
	for i, id := range wantIDs {
		if list[i].ID != id {
			t.Errorf("list[%d] = %s, want %s", i, list[i].ID, id)
		}
	}

	// После удаления текущей текущей становится предыдущая.
	if err := repo.SoftDelete(ctx, newest.ID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	current, err = repo.GetCurrentByUser(ctx, "user-42")
	if err != nil {
		t.Fatalf("get current after delete: %v", err)
	}
	if current.ID != middle.ID {
		t.Errorf("current after delete = %s, want %s", current.ID, middle.ID)
	}
	list, err = repo.ListByUser(ctx, "user-42")
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("list after delete has %d items, want 2", len(list))
	}
}

func TestAvatarRepositoryCurrentOrderTieBreak(t *testing.T) {
	pool := testPool(t)
	repo := NewAvatarRepository(pool)

	// Одинаковый created_at: побеждает больший id.
	at := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	a := createAvatarAt(t, pool, repo, "user-42", at)
	b := createAvatarAt(t, pool, repo, "user-42", at)
	want := a.ID
	if b.ID.String() > a.ID.String() {
		want = b.ID
	}

	current, err := repo.GetCurrentByUser(context.Background(), "user-42")
	if err != nil {
		t.Fatalf("get current: %v", err)
	}
	if current.ID != want {
		t.Errorf("current = %s, want %s", current.ID, want)
	}
}

func TestAvatarRepositoryEmptyUser(t *testing.T) {
	repo := NewAvatarRepository(testPool(t))
	ctx := context.Background()

	if _, err := repo.GetCurrentByUser(ctx, "nobody"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get current: err = %v, want domain.ErrNotFound", err)
	}

	list, err := repo.ListByUser(ctx, "nobody")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list == nil {
		t.Error("list is nil, want empty slice")
	}
	if len(list) != 0 {
		t.Errorf("list has %d items, want 0", len(list))
	}
}
