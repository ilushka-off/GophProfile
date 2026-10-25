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
	"net/http"
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
	byID             *domain.Avatar
	gotID            uuid.UUID
	gotUserID        string
}

func (f *fakeRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Avatar, error) {
	*f.log = append(*f.log, "repo.GetByID")
	f.gotID = id
	return f.byID, f.err
}

func (f *fakeRepo) GetCurrentByUser(_ context.Context, userID string) (*domain.Avatar, error) {
	*f.log = append(*f.log, "repo.GetCurrentByUser")
	f.gotUserID = userID
	return f.byID, f.err
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

	obj           *domain.Object
	downloadErr   error
	downloadedKey string
}

func (f *fakeStorage) Download(_ context.Context, key string) (*domain.Object, error) {
	*f.log = append(*f.log, "storage.Download")
	f.downloadedKey = key
	return f.obj, f.downloadErr
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

// webp64x48 — минимальный lossless WebP 64x48: в стандартной библиотеке нет
// кодировщика WebP, а для DetectContentType и DecodeConfig хватает заголовка VP8L.
func webp64x48(*testing.T) []byte {
	return []byte("RIFF\x12\x00\x00\x00WEBP" +
		"VP8L\x05\x00\x00\x00" +
		"\x2f\x3f\xc0\x0b\x00\x00") // сигнатура, (w-1)|(h-1)<<14, байт выравнивания
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
		{"webp", webp64x48, "image/webp", ".webp"},
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

// objectOf оборачивает данные в объект S3 и запоминает, закрыли ли его тело.
func objectOf(data []byte, contentType string) (*domain.Object, *closeTracker) {
	body := &closeTracker{Reader: bytes.NewReader(data)}
	return &domain.Object{Body: body, Size: int64(len(data)), ContentType: contentType}, body
}

type closeTracker struct {
	io.Reader
	closed bool
}

func (c *closeTracker) Close() error {
	c.closed = true
	return nil
}

func TestAvatarServiceResolveFile(t *testing.T) {
	id := uuid.New()
	thumbKey := domain.ThumbnailKey(id, domain.ThumbnailSize100)
	original := &domain.Avatar{ID: id, MimeType: "image/png", S3Key: domain.OriginalKey(id, "png")}
	withThumbs := &domain.Avatar{
		ID: id, MimeType: "image/png", S3Key: domain.OriginalKey(id, "png"),
		ThumbnailS3Keys: map[string]string{domain.ThumbnailSize100: thumbKey},
	}

	tests := []struct {
		name          string
		avatar        *domain.Avatar
		size          string
		wantKey       string
		wantType      string
		wantImmutable bool
	}{
		{"original, content type comes from the database", original, domain.SizeOriginal, original.S3Key, "image/png", true},
		{"thumbnail not ready falls back to original", original, domain.ThumbnailSize100, original.S3Key, "image/png", false},
		{"ready thumbnail", withThumbs, domain.ThumbnailSize100, thumbKey, "image/jpeg", true},
		{"other thumbnail not ready", withThumbs, domain.ThumbnailSize300, withThumbs.S3Key, "image/png", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDeps()
			d.repo.byID = tt.avatar
			d.storage.obj, _ = objectOf([]byte("x"), "binary/octet-stream")

			f, err := d.svc.ResolveFile(context.Background(), id, FileOptions{Size: tt.size})
			if err != nil {
				t.Fatalf("resolve file: %v", err)
			}
			if got := strings.Join(*d.log, ","); got != "repo.GetByID" {
				t.Errorf("calls = %s, want only repo.GetByID: resolve must not touch s3", got)
			}
			if f.ContentType != tt.wantType || f.Immutable != tt.wantImmutable {
				t.Errorf("file type %q, immutable %v; want %q, %v", f.ContentType, f.Immutable, tt.wantType, tt.wantImmutable)
			}

			obj, err := d.svc.Open(context.Background(), f)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if d.storage.downloadedKey != tt.wantKey {
				t.Errorf("downloaded key %q, want %q", d.storage.downloadedKey, tt.wantKey)
			}
			if obj.ContentType != tt.wantType || obj.Size != 1 {
				t.Errorf("object type %q, size %d; want %q, 1", obj.ContentType, obj.Size, tt.wantType)
			}
		})
	}

	t.Run("not found", func(t *testing.T) {
		d := newDeps()
		d.repo.err = domain.ErrNotFound

		if _, err := d.svc.ResolveFile(context.Background(), id, FileOptions{Size: domain.SizeOriginal}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want %v", err, domain.ErrNotFound)
		}
	})

	t.Run("missing file in s3 is passed through", func(t *testing.T) {
		d := newDeps()
		d.repo.byID = original
		d.storage.downloadErr = domain.ErrObjectNotFound

		f, err := d.svc.ResolveFile(context.Background(), id, FileOptions{Size: domain.SizeOriginal})
		if err != nil {
			t.Fatalf("resolve file: %v", err)
		}
		if _, err := d.svc.Open(context.Background(), f); !errors.Is(err, domain.ErrObjectNotFound) {
			t.Fatalf("err = %v, want %v", err, domain.ErrObjectNotFound)
		}
	})
}

func TestAvatarServiceETag(t *testing.T) {
	id := uuid.New()
	a := &domain.Avatar{
		ID: id, MimeType: "image/png", S3Key: domain.OriginalKey(id, "png"),
		ThumbnailS3Keys: map[string]string{domain.ThumbnailSize100: domain.ThumbnailKey(id, domain.ThumbnailSize100)},
	}
	etagOf := func(opts FileOptions) string {
		t.Helper()
		d := newDeps()
		d.repo.byID = a
		f, err := d.svc.ResolveFile(context.Background(), id, opts)
		if err != nil {
			t.Fatalf("resolve file: %v", err)
		}
		return f.ETag
	}

	original := etagOf(FileOptions{Size: domain.SizeOriginal})
	if !strings.HasPrefix(original, `"`) || !strings.HasSuffix(original, `"`) {
		t.Errorf("etag %s is not a quoted string", original)
	}
	if got := etagOf(FileOptions{Size: domain.SizeOriginal}); got != original {
		t.Errorf("etag changed between requests: %s, %s", original, got)
	}
	if got := etagOf(FileOptions{Size: domain.SizeOriginal, Format: "image/png"}); got != original {
		t.Errorf("format equal to the stored one changed etag: %s, want %s", got, original)
	}
	if got := etagOf(FileOptions{Size: domain.ThumbnailSize100}); got == original {
		t.Error("thumbnail has the same etag as original")
	}
	converted := etagOf(FileOptions{Size: domain.SizeOriginal, Format: "image/webp"})
	if converted == original || !strings.HasPrefix(converted, `W/"`) {
		t.Errorf("converted etag = %s, want a weak etag different from %s", converted, original)
	}
}

func TestAvatarServiceOpenFormat(t *testing.T) {
	id := uuid.New()
	pngData := encodePNG(t, 64, 48)
	stored := &domain.Avatar{ID: id, MimeType: "image/png", S3Key: domain.OriginalKey(id, "png")}

	open := func(t *testing.T, d *deps, format string) (*domain.Object, error) {
		t.Helper()
		f, err := d.svc.ResolveFile(context.Background(), id, FileOptions{Size: domain.SizeOriginal, Format: format})
		if err != nil {
			t.Fatalf("resolve file: %v", err)
		}
		return d.svc.Open(context.Background(), f)
	}

	t.Run("same format is streamed as is", func(t *testing.T) {
		d := newDeps()
		d.repo.byID = stored
		d.storage.obj, _ = objectOf(pngData, "image/png")

		obj, err := open(t, d, "image/png")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if obj != d.storage.obj {
			t.Error("object was re-encoded, want the one from s3")
		}
	})

	for _, target := range []string{"image/jpeg", "image/webp"} {
		t.Run("convert to "+target, func(t *testing.T) {
			d := newDeps()
			d.repo.byID = stored
			var src *closeTracker
			d.storage.obj, src = objectOf(pngData, "image/png")

			obj, err := open(t, d, target)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer obj.Body.Close()

			if !src.closed {
				t.Error("s3 body was not closed after decoding")
			}
			if obj.ContentType != target || obj.Size != -1 {
				t.Errorf("object type %q, size %d; want %q, -1", obj.ContentType, obj.Size, target)
			}
			data, err := io.ReadAll(obj.Body)
			if err != nil {
				t.Fatalf("read converted: %v", err)
			}
			if got := http.DetectContentType(data); got != target {
				t.Errorf("converted to %q, want %q", got, target)
			}
			cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
			if err != nil || cfg.Width != 64 || cfg.Height != 48 {
				t.Errorf("converted image %dx%d, err %v; want 64x48", cfg.Width, cfg.Height, err)
			}
		})
	}

	t.Run("broken stored file", func(t *testing.T) {
		d := newDeps()
		d.repo.byID = stored
		var src *closeTracker
		d.storage.obj, src = objectOf([]byte("\x89PNG\r\n\x1a\nbroken"), "image/png")

		if _, err := open(t, d, "image/jpeg"); err == nil {
			t.Fatal("expected decode error")
		}
		if !src.closed {
			t.Error("s3 body was not closed")
		}
	})

	t.Run("client stops reading", func(t *testing.T) {
		d := newDeps()
		d.repo.byID = stored
		d.storage.obj, _ = objectOf(encodePNG(t, 512, 512), "image/png")

		obj, err := open(t, d, "image/webp")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		// Кодировщик пишет в pipe: после Close его запись падает и горутина завершается.
		if err := obj.Body.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		if _, err := obj.Body.Read(make([]byte, 1)); !errors.Is(err, io.ErrClosedPipe) {
			t.Errorf("read after close: err = %v, want %v", err, io.ErrClosedPipe)
		}
	})
}

func TestAvatarServiceResolveCurrentFile(t *testing.T) {
	id := uuid.New()

	t.Run("looks up by user and is revalidated", func(t *testing.T) {
		d := newDeps()
		d.repo.byID = &domain.Avatar{ID: id, MimeType: "image/jpeg", S3Key: domain.OriginalKey(id, "jpg")}
		d.storage.obj, _ = objectOf([]byte("x"), "")

		f, err := d.svc.ResolveCurrentFile(context.Background(), "user-42", FileOptions{Size: domain.SizeOriginal})
		if err != nil {
			t.Fatalf("resolve current file: %v", err)
		}
		if d.repo.gotUserID != "user-42" {
			t.Errorf("looked up user %q, want user-42", d.repo.gotUserID)
		}
		if f.Immutable {
			t.Error("current avatar is immutable, but it changes after a new upload")
		}
		obj, err := d.svc.Open(context.Background(), f)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if d.storage.downloadedKey != d.repo.byID.S3Key || obj.ContentType != "image/jpeg" {
			t.Errorf("downloaded key %q, type %q", d.storage.downloadedKey, obj.ContentType)
		}
	})

	t.Run("no avatars", func(t *testing.T) {
		d := newDeps()
		d.repo.err = domain.ErrNotFound

		if _, err := d.svc.ResolveCurrentFile(context.Background(), "nobody", FileOptions{Size: domain.SizeOriginal}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want %v", err, domain.ErrNotFound)
		}
		if got := strings.Join(*d.log, ","); got != "repo.GetCurrentByUser" {
			t.Errorf("calls = %s, want only repo.GetCurrentByUser", got)
		}
	})
}
