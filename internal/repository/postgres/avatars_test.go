package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/services"
	"go-avatar-service/internal/worker"
)

var (
	_ services.AvatarRepository = (*AvatarRepository)(nil)
	_ worker.Repository         = (*AvatarRepository)(nil)
	_ worker.MessageLog         = (*MessageLog)(nil)
)

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

// createAvatar создаёт аватарку пользователя userID.
func (s *RepositorySuite) createAvatar(userID string) *domain.Avatar {
	a := newTestAvatar(userID)
	s.Require().NoError(s.repo.Create(context.Background(), a), "create avatar")
	return a
}

// createAvatarAt создаёт аватарку и выставляет ей created_at, чтобы порядок
// в тестах не зависел от того, насколько быстро прошли вставки.
func (s *RepositorySuite) createAvatarAt(userID string, createdAt time.Time) *domain.Avatar {
	a := s.createAvatar(userID)
	_, err := s.pool.Exec(context.Background(), `UPDATE avatars SET created_at = $2 WHERE id = $1`, a.ID, createdAt)
	s.Require().NoError(err, "set created_at")
	a.CreatedAt = createdAt
	return a
}

func (s *RepositorySuite) TestCreateAndGet() {
	ctx := context.Background()

	a := newTestAvatar("user-42")
	a.ThumbnailS3Keys = map[string]string{"100x100": "thumbnails/x/100x100.jpg"}
	s.Require().NoError(s.repo.Create(ctx, a))
	s.NotEqual(uuid.Nil, a.ID, "create did not set id")
	s.False(a.CreatedAt.IsZero() || a.UpdatedAt.IsZero(), "create did not set created_at/updated_at")

	got, err := s.repo.GetByID(ctx, a.ID)
	s.Require().NoError(err)
	s.Equal(a.UserID, got.UserID)
	s.Equal(a.FileName, got.FileName)
	s.Equal(a.MimeType, got.MimeType)
	s.Equal(a.SizeBytes, got.SizeBytes)
	s.Equal(a.Width, got.Width)
	s.Equal(a.Height, got.Height)
	s.Equal(a.S3Key, got.S3Key)
	s.Equal(a.UploadStatus, got.UploadStatus)
	s.Equal(a.ProcessingStatus, got.ProcessingStatus)
	s.Equal(map[string]string{"100x100": "thumbnails/x/100x100.jpg"}, got.ThumbnailS3Keys)
	s.True(got.CreatedAt.Equal(a.CreatedAt), "created_at = %v, want %v", got.CreatedAt, a.CreatedAt)
	s.Nil(got.DeletedAt)
}

func (s *RepositorySuite) TestCreateDefaultStatuses() {
	ctx := context.Background()

	a := newTestAvatar("user-42")
	a.UploadStatus = ""
	a.ProcessingStatus = ""
	s.Require().NoError(s.repo.Create(ctx, a))

	got, err := s.repo.GetByID(ctx, a.ID)
	s.Require().NoError(err)
	s.Equal(domain.UploadStatusUploading, got.UploadStatus)
	s.Equal(domain.ProcessingStatusPending, got.ProcessingStatus)
}

func (s *RepositorySuite) TestCreateWithID() {
	ctx := context.Background()

	id := uuid.New()
	a := newTestAvatar("user-42")
	a.ID = id
	s.Require().NoError(s.repo.Create(ctx, a))
	s.Equal(id, a.ID, "preset id must be kept")

	_, err := s.repo.GetByID(ctx, id)
	s.NoError(err)
}

func (s *RepositorySuite) TestGetByIDNotFound() {
	_, err := s.repo.GetByID(context.Background(), uuid.New())
	s.ErrorIs(err, domain.ErrNotFound)
}

func (s *RepositorySuite) TestSoftDelete() {
	ctx := context.Background()
	a := s.createAvatar("user-42")

	s.Require().NoError(s.repo.SoftDelete(ctx, a.ID))
	_, err := s.repo.GetByID(ctx, a.ID)
	s.ErrorIs(err, domain.ErrNotFound, "get after delete")

	// Удаление мягкое: строка осталась, у неё стоит deleted_at.
	var deleted bool
	s.Require().NoError(s.pool.QueryRow(ctx,
		`SELECT deleted_at IS NOT NULL FROM avatars WHERE id = $1`, a.ID).Scan(&deleted))
	s.True(deleted, "row has no deleted_at after soft delete")

	s.ErrorIs(s.repo.SoftDelete(ctx, a.ID), domain.ErrNotFound, "second delete")
	s.ErrorIs(s.repo.SoftDelete(ctx, uuid.New()), domain.ErrNotFound, "delete missing")
}

func (s *RepositorySuite) TestRestore() {
	ctx := context.Background()
	a := s.createAvatar("user-42")

	s.ErrorIs(s.repo.Restore(ctx, a.ID), domain.ErrNotFound, "restore not deleted")
	s.Require().NoError(s.repo.SoftDelete(ctx, a.ID))
	s.Require().NoError(s.repo.Restore(ctx, a.ID))

	_, err := s.repo.GetByID(ctx, a.ID)
	s.NoError(err, "get after restore")
	s.ErrorIs(s.repo.Restore(ctx, uuid.New()), domain.ErrNotFound, "restore missing")
}

func (s *RepositorySuite) TestDelete() {
	ctx := context.Background()
	a := s.createAvatar("user-42")

	s.Require().NoError(s.repo.Delete(ctx, a.ID))
	var n int
	s.Require().NoError(s.pool.QueryRow(ctx, `SELECT count(*) FROM avatars WHERE id = $1`, a.ID).Scan(&n))
	s.Zero(n, "row is still there")
	s.NoError(s.repo.Delete(ctx, a.ID), "delete missing")
}

func (s *RepositorySuite) TestCurrentAndList() {
	ctx := context.Background()

	base := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	oldest := s.createAvatarAt("user-42", base)
	middle := s.createAvatarAt("user-42", base.Add(time.Minute))
	newest := s.createAvatarAt("user-42", base.Add(2*time.Minute))
	s.createAvatarAt("user-7", base.Add(3*time.Minute))

	current, err := s.repo.GetCurrentByUser(ctx, "user-42")
	s.Require().NoError(err)
	s.Equal(newest.ID, current.ID, "current must be the newest")

	list, err := s.repo.ListByUser(ctx, "user-42")
	s.Require().NoError(err)
	s.Equal([]uuid.UUID{newest.ID, middle.ID, oldest.ID}, ids(list))

	// После удаления текущей текущей становится предыдущая.
	s.Require().NoError(s.repo.SoftDelete(ctx, newest.ID))
	current, err = s.repo.GetCurrentByUser(ctx, "user-42")
	s.Require().NoError(err)
	s.Equal(middle.ID, current.ID, "current after delete")

	list, err = s.repo.ListByUser(ctx, "user-42")
	s.Require().NoError(err)
	s.Equal([]uuid.UUID{middle.ID, oldest.ID}, ids(list))
}

func (s *RepositorySuite) TestCurrentOrderTieBreak() {
	// Одинаковый created_at: побеждает больший id.
	at := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	a := s.createAvatarAt("user-42", at)
	b := s.createAvatarAt("user-42", at)
	want := a.ID
	if b.ID.String() > a.ID.String() {
		want = b.ID
	}

	current, err := s.repo.GetCurrentByUser(context.Background(), "user-42")
	s.Require().NoError(err)
	s.Equal(want, current.ID)
}

func (s *RepositorySuite) TestEmptyUser() {
	ctx := context.Background()

	_, err := s.repo.GetCurrentByUser(ctx, "nobody")
	s.ErrorIs(err, domain.ErrNotFound)

	list, err := s.repo.ListByUser(ctx, "nobody")
	s.Require().NoError(err)
	s.NotNil(list, "list must be an empty slice, not nil")
	s.Empty(list)
}

func (s *RepositorySuite) TestSetProcessingStatus() {
	ctx := context.Background()
	a := s.createAvatar("user-42")

	s.Require().NoError(s.repo.SetProcessingStatus(ctx, a.ID, domain.ProcessingStatusProcessing))
	got, err := s.repo.GetByID(ctx, a.ID)
	s.Require().NoError(err)
	s.Equal(domain.ProcessingStatusProcessing, got.ProcessingStatus)
	s.True(got.UpdatedAt.After(a.UpdatedAt) || got.UpdatedAt.Equal(a.UpdatedAt))

	s.ErrorIs(s.repo.SetProcessingStatus(ctx, uuid.New(), domain.ProcessingStatusFailed), domain.ErrNotFound)

	s.Require().NoError(s.repo.SoftDelete(ctx, a.ID))
	s.ErrorIs(s.repo.SetProcessingStatus(ctx, a.ID, domain.ProcessingStatusFailed), domain.ErrNotFound,
		"deleted avatar must not change")
}

func (s *RepositorySuite) TestUnknownStatusRejected() {
	ctx := context.Background()
	a := s.createAvatar("user-42")

	s.Error(s.repo.SetProcessingStatus(ctx, a.ID, domain.ProcessingStatus("done")),
		"processing_status must reject values outside the enum")

	b := newTestAvatar("user-42")
	b.UploadStatus = domain.UploadStatus("done")
	s.Error(s.repo.Create(ctx, b), "upload_status must reject values outside the enum")
}

func (s *RepositorySuite) TestCompleteProcessing() {
	ctx := context.Background()
	a := newTestAvatar("user-42")
	a.ThumbnailS3Keys = map[string]string{"100x100": "old/100.jpg", "300x300": "old/300.jpg"}
	s.Require().NoError(s.repo.Create(ctx, a))

	// Новые ключи дописываются к старым, а не заменяют весь объект.
	s.Require().NoError(s.repo.CompleteProcessing(ctx, a.ID, map[string]string{"100x100": "new/100.jpg"}))

	got, err := s.repo.GetByID(ctx, a.ID)
	s.Require().NoError(err)
	s.Equal(domain.ProcessingStatusCompleted, got.ProcessingStatus)
	s.Equal(map[string]string{"100x100": "new/100.jpg", "300x300": "old/300.jpg"}, got.ThumbnailS3Keys)

	// Пустой thumbnail_s3_keys (NULL) тоже дополняется.
	b := s.createAvatar("user-42")
	s.Require().NoError(s.repo.CompleteProcessing(ctx, b.ID, map[string]string{"300x300": "t/300.jpg"}))
	got, err = s.repo.GetByID(ctx, b.ID)
	s.Require().NoError(err)
	s.Equal(map[string]string{"300x300": "t/300.jpg"}, got.ThumbnailS3Keys)

	s.Require().NoError(s.repo.SoftDelete(ctx, b.ID))
	s.ErrorIs(s.repo.CompleteProcessing(ctx, b.ID, map[string]string{}), domain.ErrNotFound)
}

func (s *RepositorySuite) TestMessageLog() {
	ctx := context.Background()
	log := NewMessageLog(s.pool)

	done, err := log.IsProcessed(ctx, "m1")
	s.Require().NoError(err)
	s.False(done)

	s.Require().NoError(log.MarkProcessed(ctx, "m1", "avatar.uploaded"))
	s.Require().NoError(log.MarkProcessed(ctx, "m1", "avatar.uploaded"), "second mark is not an error")

	done, err = log.IsProcessed(ctx, "m1")
	s.Require().NoError(err)
	s.True(done)
}

func ids(avatars []domain.Avatar) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(avatars))
	for i := range avatars {
		out = append(out, avatars[i].ID)
	}
	return out
}
