package worker

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/google/uuid"

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/domain"
)

func deletedMessage(t *testing.T, keys ...string) broker.Message {
	t.Helper()
	body, err := json.Marshal(broker.AvatarDeleteEvent{AvatarID: testID.String(), S3Keys: keys})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return broker.Message{ID: uuid.NewString(), Type: broker.TypeAvatarDeleted, Key: testID.String(), Body: body}
}

func processMessage(t *testing.T, ops ...broker.ProcessingOp) broker.Message {
	t.Helper()
	body, err := json.Marshal(broker.AvatarProcessEvent{AvatarID: testID.String(), Operations: ops})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return broker.Message{ID: uuid.NewString(), Type: broker.TypeAvatarProcess, Key: testID.String(), Body: body}
}

func TestProcessorDeletesFiles(t *testing.T) {
	p, _, storage := setup(t)
	keys := []string{"originals/a.png", "thumbnails/a/100x100.jpg", "thumbnails/a/300x300.jpg"}

	// Дважды: повторная доставка — не ошибка.
	for range 2 {
		if err := p.Handle(context.Background(), deletedMessage(t, keys...)); err != nil {
			t.Fatalf("Handle: %v", err)
		}
	}
	if want := append(slices.Clone(keys), keys...); !slices.Equal(storage.deleted, want) {
		t.Errorf("deleted = %v, want %v", storage.deleted, want)
	}
}

func TestProcessorDeleteErrors(t *testing.T) {
	t.Run("s3 down is retryable", func(t *testing.T) {
		p, _, storage := setup(t)
		storage.deleteErr = errors.New("connection refused")

		err := p.Handle(context.Background(), deletedMessage(t, "originals/a.png"))
		if !errors.Is(err, broker.ErrRetryable) {
			t.Errorf("err = %v, want retryable", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		p, _, _ := setup(t)
		err := p.Handle(context.Background(), broker.Message{Type: broker.TypeAvatarDeleted, Body: []byte("[")})
		if !errors.Is(err, ErrBadMessage) {
			t.Errorf("err = %v, want ErrBadMessage", err)
		}
	})
}

func TestProcessorReprocess(t *testing.T) {
	t.Run("regenerates requested size even when completed", func(t *testing.T) {
		p, repo, _ := setup(t)
		repo.avatar.ProcessingStatus = domain.ProcessingStatusCompleted

		msg := processMessage(t, broker.ProcessingOp{Type: broker.OpThumbnail, Size: domain.ThumbnailSize100})
		if err := p.Handle(context.Background(), msg); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		want := map[string]string{"100x100": domain.ThumbnailKey(testID, domain.ThumbnailSize100)}
		if !maps.Equal(repo.completed, want) {
			t.Errorf("completed = %v, want %v", repo.completed, want)
		}
	})

	t.Run("no operations means all sizes", func(t *testing.T) {
		p, repo, _ := setup(t)
		if err := p.Handle(context.Background(), processMessage(t)); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		if len(repo.completed) != len(domain.ThumbnailSizes) {
			t.Errorf("completed = %v, want all sizes", repo.completed)
		}
	})

	t.Run("unsupported operation", func(t *testing.T) {
		p, _, _ := setup(t)
		msg := processMessage(t, broker.ProcessingOp{Type: "crop"})
		if err := p.Handle(context.Background(), msg); !errors.Is(err, ErrBadMessage) {
			t.Errorf("err = %v, want ErrBadMessage", err)
		}
	})

	t.Run("deleted avatar is skipped", func(t *testing.T) {
		p, repo, storage := setup(t)
		repo.avatar = nil
		if err := p.Handle(context.Background(), processMessage(t)); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		if len(storage.uploaded) != 0 {
			t.Error("deleted avatar was processed")
		}
	})

	t.Run("failure is marked", func(t *testing.T) {
		p, repo, storage := setup(t)
		clear(storage.objects)
		err := p.MarkFailed(p.Handle)(context.Background(), processMessage(t))
		if !errors.Is(err, ErrBrokenImage) {
			t.Fatalf("err = %v, want ErrBrokenImage", err)
		}
		if got := repo.statuses[len(repo.statuses)-1]; got != domain.ProcessingStatusFailed {
			t.Errorf("last status = %s, want failed", got)
		}
	})
}
