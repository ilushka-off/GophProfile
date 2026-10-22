package services

import (
	"context"
	"io"

	"github.com/google/uuid"

	"go-avatar-service/internal/domain"
)

// AvatarRepository — хранилище метаданных аватарок, которое нужно сервису.
type AvatarRepository interface {
	Create(ctx context.Context, a *domain.Avatar) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Avatar, error)
	GetCurrentByUser(ctx context.Context, userID string) (*domain.Avatar, error)
	ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error)
	SoftDelete(ctx context.Context, id uuid.UUID) error
}

// Storage — файловое хранилище (S3), которое нужно сервису.
type Storage interface {
	// Upload кладёт файл по ключу. size — размер в байтах, -1 если неизвестен.
	Upload(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Download отдаёт файл потоком или domain.ErrObjectNotFound. Body закрывает вызывающий.
	Download(ctx context.Context, key string) (*domain.Object, error)
	// Delete удаляет файл. Отсутствующий файл — не ошибка.
	Delete(ctx context.Context, key string) error
	BucketExists(ctx context.Context) (bool, error)
}
