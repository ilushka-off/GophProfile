package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/domain"
)

var testID = uuid.MustParse("0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10")

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeRepo struct {
	mu          sync.Mutex
	avatar      *domain.Avatar
	getErr      error
	setErr      error
	completeErr error

	statuses  []domain.ProcessingStatus
	completed map[string]string
}

func (f *fakeRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Avatar, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.avatar == nil || f.avatar.ID != id {
		return nil, domain.ErrNotFound
	}
	a := *f.avatar
	return &a, nil
}

func (f *fakeRepo) SetProcessingStatus(_ context.Context, _ uuid.UUID, status domain.ProcessingStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, status)
	return f.setErr
}

func (f *fakeRepo) CompleteProcessing(_ context.Context, _ uuid.UUID, thumbnails map[string]string) error {
	f.completed = thumbnails
	return f.completeErr
}

type fakeStorage struct {
	objects     map[string][]byte
	downloadErr error
	uploadErr   error
	deleteErr   error

	uploaded map[string][]byte
	deleted  []string
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{objects: map[string][]byte{}, uploaded: map[string][]byte{}}
}

func (f *fakeStorage) Upload(_ context.Context, key string, r io.Reader, size int64, contentType string) error {
	if f.uploadErr != nil {
		return f.uploadErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if int64(len(data)) != size || contentType != domain.ThumbnailMimeType {
		return errors.New("unexpected size or content type")
	}
	f.uploaded[key] = data
	return nil
}

func (f *fakeStorage) Download(_ context.Context, key string) (*domain.Object, error) {
	if f.downloadErr != nil {
		return nil, f.downloadErr
	}
	data, ok := f.objects[key]
	if !ok {
		return nil, domain.ErrObjectNotFound
	}
	return &domain.Object{Body: io.NopCloser(bytes.NewReader(data)), Size: int64(len(data))}, nil
}

func (f *fakeStorage) Delete(_ context.Context, key string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, key)
	return nil
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// setup — аватарка в статусе pending и её PNG-оригинал в хранилище.
func setup(t *testing.T) (*Processor, *fakeRepo, *fakeStorage) {
	t.Helper()
	key := domain.OriginalKey(testID, "png")
	repo := &fakeRepo{avatar: &domain.Avatar{
		ID:               testID,
		UserID:           "user-42",
		S3Key:            key,
		ProcessingStatus: domain.ProcessingStatusPending,
	}}
	storage := newFakeStorage()
	storage.objects[key] = pngBytes(t, 640, 480)
	return NewProcessor(repo, storage, discardLogger()), repo, storage
}

func uploadedMessage(t *testing.T) broker.Message {
	t.Helper()
	body, err := json.Marshal(broker.AvatarUploadEvent{
		AvatarID: testID.String(),
		UserID:   "user-42",
		S3Key:    domain.OriginalKey(testID, "png"),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return broker.Message{ID: uuid.NewString(), Type: broker.TypeAvatarUploaded, Key: testID.String(), Body: body}
}

func TestProcessorCreatesThumbnails(t *testing.T) {
	p, repo, storage := setup(t)

	if err := p.Handle(context.Background(), uploadedMessage(t)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	wantKeys := map[string]string{
		"100x100": "thumbnails/0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10/100x100.jpg",
		"300x300": "thumbnails/0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10/300x300.jpg",
	}
	if !maps.Equal(repo.completed, wantKeys) {
		t.Errorf("completed keys = %v, want %v", repo.completed, wantKeys)
	}
	if want := []domain.ProcessingStatus{domain.ProcessingStatusProcessing}; !slices.Equal(repo.statuses, want) {
		t.Errorf("statuses = %v, want %v", repo.statuses, want)
	}
	for size, key := range wantKeys {
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(storage.uploaded[key]))
		if err != nil {
			t.Fatalf("thumbnail %s is not a jpeg: %v", size, err)
		}
		if side := thumbnailSides[size]; cfg.Width != side || cfg.Height != side {
			t.Errorf("thumbnail %s is %dx%d", size, cfg.Width, cfg.Height)
		}
	}
}

func TestProcessorSkips(t *testing.T) {
	t.Run("deleted avatar", func(t *testing.T) {
		p, repo, storage := setup(t)
		repo.avatar = nil

		if err := p.Handle(context.Background(), uploadedMessage(t)); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		if len(storage.uploaded) != 0 || len(repo.statuses) != 0 {
			t.Errorf("deleted avatar was processed: uploads %d, statuses %v", len(storage.uploaded), repo.statuses)
		}
	})

	t.Run("already completed", func(t *testing.T) {
		p, repo, storage := setup(t)
		repo.avatar.ProcessingStatus = domain.ProcessingStatusCompleted

		if err := p.Handle(context.Background(), uploadedMessage(t)); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		if len(storage.uploaded) != 0 || len(repo.statuses) != 0 {
			t.Errorf("completed avatar was processed again")
		}
	})

	t.Run("deleted during processing removes thumbnails", func(t *testing.T) {
		p, repo, storage := setup(t)
		repo.completeErr = domain.ErrNotFound

		if err := p.Handle(context.Background(), uploadedMessage(t)); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		if len(storage.deleted) != 2 {
			t.Errorf("deleted = %v, want both thumbnails", storage.deleted)
		}
	})
}

func TestProcessorErrors(t *testing.T) {
	errDown := errors.New("connection refused")

	tests := []struct {
		name          string
		setup         func(*fakeRepo, *fakeStorage)
		msg           func(t *testing.T) broker.Message
		wantErr       error
		wantRetryable bool
	}{
		{
			name:    "unknown type",
			msg:     func(*testing.T) broker.Message { return broker.Message{Type: "avatar.unknown"} },
			wantErr: ErrUnknownEvent,
		},
		{
			name: "bad json",
			msg: func(*testing.T) broker.Message {
				return broker.Message{Type: broker.TypeAvatarUploaded, Body: []byte("{")}
			},
			wantErr: ErrBadMessage,
		},
		{
			name: "bad avatar id",
			msg: func(*testing.T) broker.Message {
				return broker.Message{Type: broker.TypeAvatarUploaded, Body: []byte(`{"avatar_id":"x"}`)}
			},
			wantErr: ErrBadMessage,
		},
		{
			name:          "db down",
			setup:         func(r *fakeRepo, _ *fakeStorage) { r.getErr = errDown },
			wantErr:       errDown,
			wantRetryable: true,
		},
		{
			name:          "status update fails",
			setup:         func(r *fakeRepo, _ *fakeStorage) { r.setErr = errDown },
			wantErr:       errDown,
			wantRetryable: true,
		},
		{
			name:          "s3 down",
			setup:         func(_ *fakeRepo, s *fakeStorage) { s.downloadErr = errDown },
			wantErr:       errDown,
			wantRetryable: true,
		},
		{
			name:    "original missing",
			setup:   func(_ *fakeRepo, s *fakeStorage) { clear(s.objects) },
			wantErr: ErrBrokenImage,
		},
		{
			name: "broken image",
			setup: func(_ *fakeRepo, s *fakeStorage) {
				s.objects[domain.OriginalKey(testID, "png")] = []byte("\x89PNG\r\n\x1a\nbroken")
			},
			wantErr: ErrBrokenImage,
		},
		{
			name:          "thumbnail upload fails",
			setup:         func(_ *fakeRepo, s *fakeStorage) { s.uploadErr = errDown },
			wantErr:       errDown,
			wantRetryable: true,
		},
		{
			name:          "complete fails",
			setup:         func(r *fakeRepo, _ *fakeStorage) { r.completeErr = errDown },
			wantErr:       errDown,
			wantRetryable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, repo, storage := setup(t)
			if tt.setup != nil {
				tt.setup(repo, storage)
			}
			msg := uploadedMessage(t)
			if tt.msg != nil {
				msg = tt.msg(t)
			}

			err := p.Handle(context.Background(), msg)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got := errors.Is(err, broker.ErrRetryable); got != tt.wantRetryable {
				t.Errorf("retryable = %v, want %v", got, tt.wantRetryable)
			}
		})
	}
}

func TestMarkFailed(t *testing.T) {
	failing := func(context.Context, broker.Message) error { return ErrBrokenImage }
	ok := func(context.Context, broker.Message) error { return nil }

	t.Run("failed upload is marked", func(t *testing.T) {
		p, repo, _ := setup(t)
		err := p.MarkFailed(failing)(context.Background(), uploadedMessage(t))

		if !errors.Is(err, ErrBrokenImage) {
			t.Errorf("err = %v, want it passed through", err)
		}
		if want := []domain.ProcessingStatus{domain.ProcessingStatusFailed}; !slices.Equal(repo.statuses, want) {
			t.Errorf("statuses = %v, want %v", repo.statuses, want)
		}
	})

	t.Run("success is not marked", func(t *testing.T) {
		p, repo, _ := setup(t)
		if err := p.MarkFailed(ok)(context.Background(), uploadedMessage(t)); err != nil {
			t.Fatal(err)
		}
		if len(repo.statuses) != 0 {
			t.Errorf("statuses = %v, want none", repo.statuses)
		}
	})

	t.Run("delete event is not marked", func(t *testing.T) {
		p, repo, _ := setup(t)
		msg := broker.Message{Type: broker.TypeAvatarDeleted, Key: testID.String()}
		_ = p.MarkFailed(failing)(context.Background(), msg)
		if len(repo.statuses) != 0 {
			t.Errorf("statuses = %v, want none", repo.statuses)
		}
	})

	t.Run("shutdown is not a failure", func(t *testing.T) {
		p, repo, _ := setup(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = p.MarkFailed(failing)(ctx, uploadedMessage(t))
		if len(repo.statuses) != 0 {
			t.Errorf("statuses = %v, want none", repo.statuses)
		}
	})
}
