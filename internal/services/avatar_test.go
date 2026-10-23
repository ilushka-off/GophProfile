package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/domain"
)

const testMaxUploadSize = 10 << 20

// calls — общий журнал вызовов зависимостей, чтобы проверять порядок S3 → БД → брокер.
type calls []string

type fakeRepo struct {
	AvatarRepository // остальные методы в этих тестах не вызываются
	log              *calls
	err              error
	created          *domain.Avatar
}

func (f *fakeRepo) Create(_ context.Context, a *domain.Avatar) error {
	*f.log = append(*f.log, "repo.Create")
	if f.err != nil {
		return f.err
	}
	f.created = a
	return nil
}

type fakeStorage struct {
	Storage
	log       *calls
	uploadErr error

	key         string
	body        []byte
	size        int64
	contentType string
	deleted     []string
}

func (f *fakeStorage) Upload(_ context.Context, key string, r io.Reader, size int64, contentType string) error {
	*f.log = append(*f.log, "storage.Upload")
	if f.uploadErr != nil {
		return f.uploadErr
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.key, f.body, f.size, f.contentType = key, body, size, contentType
	return nil
}

func (f *fakeStorage) Delete(_ context.Context, key string) error {
	*f.log = append(*f.log, "storage.Delete")
	f.deleted = append(f.deleted, key)
	return nil
}

type fakePublisher struct {
	log  *calls
	err  error
	msgs []broker.Message
}

func (f *fakePublisher) Publish(_ context.Context, msg broker.Message) error {
	*f.log = append(*f.log, "publisher.Publish")
	f.msgs = append(f.msgs, msg)
	return f.err
}

type deps struct {
	log       *calls
	repo      *fakeRepo
	storage   *fakeStorage
	publisher *fakePublisher
	svc       *AvatarService
}

func newDeps() *deps {
	log := &calls{}
	d := &deps{
		log:       log,
		repo:      &fakeRepo{log: log},
		storage:   &fakeStorage{log: log},
		publisher: &fakePublisher{log: log},
	}
	d.svc = NewAvatarService(d.repo, d.storage, d.publisher, testMaxUploadSize)
	return d
}

func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func encodeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func uploadInput(data []byte) UploadInput {
	return UploadInput{
		UserID:   "user-42",
		FileName: "me.png",
		File:     bytes.NewReader(data),
		Size:     int64(len(data)),
	}
}

func TestAvatarServiceUpload(t *testing.T) {
	tests := []struct {
		name     string
		data     func(t *testing.T) []byte
		wantMime string
		wantExt  string
	}{
		{"png", func(t *testing.T) []byte { return encodePNG(t, 64, 48) }, "image/png", ".png"},
		{"jpeg", func(t *testing.T) []byte { return encodeJPEG(t, 64, 48) }, "image/jpeg", ".jpg"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDeps()
			data := tt.data(t)

			a, err := d.svc.Upload(context.Background(), uploadInput(data))
			if err != nil {
				t.Fatalf("upload: %v", err)
			}

			wantOrder := "storage.Upload,repo.Create,publisher.Publish"
			if got := strings.Join(*d.log, ","); got != wantOrder {
				t.Errorf("calls = %s, want %s", got, wantOrder)
			}

			if a.ID == uuid.Nil {
				t.Fatal("avatar id is not set")
			}
			wantKey := domain.OriginalKey(a.ID, strings.TrimPrefix(tt.wantExt, "."))
			if a.S3Key != wantKey || d.storage.key != wantKey {
				t.Errorf("s3 key = %q (stored at %q), want %q", a.S3Key, d.storage.key, wantKey)
			}
			if !bytes.Equal(d.storage.body, data) {
				t.Error("stored file differs from uploaded one")
			}
			if d.storage.size != int64(len(data)) || d.storage.contentType != tt.wantMime {
				t.Errorf("stored size %d, type %q; want %d, %q", d.storage.size, d.storage.contentType, len(data), tt.wantMime)
			}

			if d.repo.created != a {
				t.Fatal("repo did not get the returned avatar")
			}
			if a.UserID != "user-42" || a.FileName != "me.png" || a.MimeType != tt.wantMime ||
				a.SizeBytes != int64(len(data)) || a.Width != 64 || a.Height != 48 {
				t.Errorf("avatar = %+v, want user-42, me.png, %s, size %d, 64x48", a, tt.wantMime, len(data))
			}
			if a.UploadStatus != domain.UploadStatusUploaded || a.ProcessingStatus != domain.ProcessingStatusPending {
				t.Errorf("statuses = %s/%s, want uploaded/pending", a.UploadStatus, a.ProcessingStatus)
			}

			msg := d.publisher.msgs[0]
			if msg.Type != broker.TypeAvatarUploaded || msg.Key != a.ID.String() {
				t.Errorf("message type %q key %q, want %q %q", msg.Type, msg.Key, broker.TypeAvatarUploaded, a.ID)
			}
			if _, err := uuid.Parse(msg.ID); err != nil {
				t.Errorf("message id %q is not a uuid", msg.ID)
			}
			var ev broker.AvatarUploadEvent
			if err := json.Unmarshal(msg.Body, &ev); err != nil {
				t.Fatalf("decode event: %v", err)
			}
			if ev != (broker.AvatarUploadEvent{AvatarID: a.ID.String(), UserID: "user-42", S3Key: wantKey}) {
				t.Errorf("event = %+v", ev)
			}
		})
	}
}

func TestAvatarServiceUploadRejects(t *testing.T) {
	tests := []struct {
		name    string
		in      func(t *testing.T) UploadInput
		wantErr error
	}{
		{
			name: "too large",
			in: func(t *testing.T) UploadInput {
				in := uploadInput(encodePNG(t, 1, 1))
				in.Size = testMaxUploadSize + 1
				return in
			},
			wantErr: domain.ErrTooLarge,
		},
		{
			name:    "not an image",
			in:      func(*testing.T) UploadInput { return uploadInput([]byte("just some text, not a picture")) },
			wantErr: domain.ErrInvalidFormat,
		},
		{
			name:    "empty file",
			in:      func(*testing.T) UploadInput { return uploadInput(nil) },
			wantErr: domain.ErrInvalidFormat,
		},
		{
			name: "unsupported image format",
			in: func(*testing.T) UploadInput {
				return uploadInput([]byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"))
			},
			wantErr: domain.ErrInvalidFormat,
		},
		{
			name: "png signature with broken header",
			in: func(*testing.T) UploadInput {
				return uploadInput([]byte("\x89PNG\r\n\x1a\nbroken"))
			},
			wantErr: domain.ErrInvalidFormat,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDeps()

			_, err := d.svc.Upload(context.Background(), tt.in(t))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if len(*d.log) != 0 {
				t.Errorf("dependencies were called: %v", *d.log)
			}
		})
	}
}

func TestAvatarServiceUploadFailures(t *testing.T) {
	errBoom := errors.New("boom")

	t.Run("storage fails", func(t *testing.T) {
		d := newDeps()
		d.storage.uploadErr = errBoom

		if _, err := d.svc.Upload(context.Background(), uploadInput(encodePNG(t, 1, 1))); !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want %v", err, errBoom)
		}
		if got := strings.Join(*d.log, ","); got != "storage.Upload" {
			t.Errorf("calls = %s, want only storage.Upload", got)
		}
	})

	t.Run("database fails, original is removed", func(t *testing.T) {
		d := newDeps()
		d.repo.err = errBoom

		if _, err := d.svc.Upload(context.Background(), uploadInput(encodePNG(t, 1, 1))); !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want %v", err, errBoom)
		}
		if got := strings.Join(*d.log, ","); got != "storage.Upload,repo.Create,storage.Delete" {
			t.Errorf("calls = %s", got)
		}
		if len(d.storage.deleted) != 1 || d.storage.deleted[0] != d.storage.key {
			t.Errorf("deleted = %v, want [%s]", d.storage.deleted, d.storage.key)
		}
	})

	t.Run("publish fails, row stays pending", func(t *testing.T) {
		d := newDeps()
		d.publisher.err = errBoom

		if _, err := d.svc.Upload(context.Background(), uploadInput(encodePNG(t, 1, 1))); !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want %v", err, errBoom)
		}
		if d.repo.created == nil || d.repo.created.ProcessingStatus != domain.ProcessingStatusPending {
			t.Error("row must be created with pending status")
		}
		if len(d.storage.deleted) != 0 {
			t.Errorf("original must stay in S3 for republishing, deleted %v", d.storage.deleted)
		}
	})
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("аватар.png", 6); got != "аватар" {
		t.Errorf("truncateRunes = %q, want %q", got, "аватар")
	}
	if got := truncateRunes("me.png", 255); got != "me.png" {
		t.Errorf("truncateRunes = %q, want unchanged", got)
	}
}
