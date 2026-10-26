package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go-avatar-service/internal/domain"
)

// avatarColumns — колонки avatars в порядке, который ожидает scanAvatar.
const avatarColumns = `id, user_id, file_name, mime_type, size_bytes, width, height, s3_key,
	thumbnail_s3_keys, upload_status, processing_status, created_at, updated_at, deleted_at`

// AvatarRepository хранит метаданные аватарок в PostgreSQL.
// Удалённые (deleted_at IS NOT NULL) аватарки для чтения не существуют.
type AvatarRepository struct {
	pool *pgxpool.Pool
}

// NewAvatarRepository создаёт репозиторий поверх пула соединений.
func NewAvatarRepository(pool *pgxpool.Pool) *AvatarRepository {
	return &AvatarRepository{pool: pool}
}

// Create вставляет аватарку и заполняет в ней id, created_at и updated_at, выданные базой.
// Если id уже задан, используется он: сервису id нужен до вставки, чтобы построить ключ S3.
// Пустые статусы заменяются умолчаниями uploading и pending, как в схеме таблицы.
func (r *AvatarRepository) Create(ctx context.Context, a *domain.Avatar) error {
	if a.UploadStatus == "" {
		a.UploadStatus = domain.UploadStatusUploading
	}
	if a.ProcessingStatus == "" {
		a.ProcessingStatus = domain.ProcessingStatusPending
	}

	var id *uuid.UUID
	if a.ID != uuid.Nil {
		id = &a.ID
	}

	err := r.pool.QueryRow(ctx, `
		INSERT INTO avatars (id, user_id, file_name, mime_type, size_bytes, width, height, s3_key,
			thumbnail_s3_keys, upload_status, processing_status)
		VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, created_at, updated_at`,
		id, a.UserID, a.FileName, a.MimeType, a.SizeBytes, a.Width, a.Height, a.S3Key,
		a.ThumbnailS3Keys, a.UploadStatus, a.ProcessingStatus,
	).Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert avatar: %w", err)
	}
	return nil
}

// GetByID возвращает неудалённую аватарку по id или domain.ErrNotFound.
func (r *AvatarRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Avatar, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+avatarColumns+`
		FROM avatars
		WHERE id = $1 AND deleted_at IS NULL`, id)

	a, err := scanAvatar(row)
	if err != nil {
		return nil, fmt.Errorf("get avatar by id: %w", err)
	}
	return a, nil
}

// GetCurrentByUser возвращает текущую аватарку пользователя — последнюю загруженную
// неудалённую — или domain.ErrNotFound, если аватарок нет.
func (r *AvatarRepository) GetCurrentByUser(ctx context.Context, userID string) (*domain.Avatar, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+avatarColumns+`
		FROM avatars
		WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, userID)

	a, err := scanAvatar(row)
	if err != nil {
		return nil, fmt.Errorf("get current avatar by user: %w", err)
	}
	return a, nil
}

// ListByUser возвращает неудалённые аватарки пользователя, новые сверху.
// Если аватарок нет, возвращает пустой слайс, а не nil: в JSON он станет [], а не null.
func (r *AvatarRepository) ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+avatarColumns+`
		FROM avatars
		WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list avatars by user: %w", err)
	}

	avatars, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Avatar, error) {
		a, err := scanAvatar(row)
		if err != nil {
			return domain.Avatar{}, err
		}
		return *a, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list avatars by user: %w", err)
	}
	if avatars == nil {
		avatars = []domain.Avatar{}
	}
	return avatars, nil
}

// SoftDelete помечает аватарку удалённой. Если её нет или она уже удалена,
// возвращает domain.ErrNotFound.
func (r *AvatarRepository) SoftDelete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE avatars
		SET deleted_at = now(), updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("soft delete avatar: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// SetProcessingStatus меняет processing_status неудалённой аватарки.
// Если её нет или она удалена, возвращает domain.ErrNotFound.
func (r *AvatarRepository) SetProcessingStatus(ctx context.Context, id uuid.UUID, status domain.ProcessingStatus) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE avatars
		SET processing_status = $2, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL`, id, status)
	if err != nil {
		return fmt.Errorf("set processing status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// CompleteProcessing добавляет ключи миниатюр к thumbnail_s3_keys (размеры,
// которых нет в thumbnails, остаются как были) и ставит processing_status = completed.
// Если аватарку успели удалить, возвращает domain.ErrNotFound.
func (r *AvatarRepository) CompleteProcessing(ctx context.Context, id uuid.UUID, thumbnails map[string]string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE avatars
		SET thumbnail_s3_keys = COALESCE(thumbnail_s3_keys, '{}'::jsonb) || $2::jsonb,
			processing_status = $3,
			updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL`, id, thumbnails, domain.ProcessingStatusCompleted)
	if err != nil {
		return fmt.Errorf("complete processing: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// scanAvatar читает строку с колонками avatarColumns.
func scanAvatar(row pgx.Row) (*domain.Avatar, error) {
	var a domain.Avatar
	err := row.Scan(
		&a.ID, &a.UserID, &a.FileName, &a.MimeType, &a.SizeBytes, &a.Width, &a.Height, &a.S3Key,
		&a.ThumbnailS3Keys, &a.UploadStatus, &a.ProcessingStatus, &a.CreatedAt, &a.UpdatedAt, &a.DeletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}
