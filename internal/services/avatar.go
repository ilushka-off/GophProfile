package services

import (
	"context"

	"github.com/google/uuid"

	"go-avatar-service/internal/domain"
)

// AvatarRepository — хранилище метаданных аватарок, которое нужно сервису.
// Объявлено здесь, на стороне потребителя, чтобы в unit-тестах подставлять мок.
type AvatarRepository interface {
	Create(ctx context.Context, a *domain.Avatar) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Avatar, error)
	GetCurrentByUser(ctx context.Context, userID string) (*domain.Avatar, error)
	ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error)
	SoftDelete(ctx context.Context, id uuid.UUID) error
}
