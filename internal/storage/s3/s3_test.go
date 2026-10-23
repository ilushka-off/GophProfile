package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/services"
)

var _ services.Storage = (*Client)(nil)

// Интеграционные тесты идут на настоящий S3 (SeaweedFS из docker compose).
// Каждый тест работает в своём новом бакете и удаляет его за собой.
const (
	endpointEnv  = "TEST_S3_ENDPOINT"
	accessKeyEnv = "TEST_S3_ACCESS_KEY"
	secretKeyEnv = "TEST_S3_SECRET_KEY"
)

func testClient(t *testing.T) *Client {
	t.Helper()

	endpoint := os.Getenv(endpointEnv)
	if endpoint == "" {
		t.Skipf("%s is not set", endpointEnv)
	}

	c, err := New(config.S3Config{
		Endpoint:  endpoint,
		Region:    "us-east-1",
		AccessKey: os.Getenv(accessKeyEnv),
		SecretKey: os.Getenv(secretKeyEnv),
		Bucket:    "test-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err := c.EnsureBucket(context.Background()); err != nil {
		t.Fatalf("ensure bucket: %v", err)
	}
	t.Cleanup(func() { removeBucket(t, c) })
	return c
}

func removeBucket(t *testing.T, c *Client) {
	t.Helper()
	ctx := context.Background()
	for obj := range c.mc.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{Recursive: true}) {
		if obj.Err != nil {
			t.Errorf("list objects: %v", obj.Err)
			return
		}
		if err := c.mc.RemoveObject(ctx, c.bucket, obj.Key, minio.RemoveObjectOptions{}); err != nil {
			t.Errorf("remove %q: %v", obj.Key, err)
		}
	}
	if err := c.mc.RemoveBucket(ctx, c.bucket); err != nil {
		t.Errorf("remove bucket: %v", err)
	}
}

func TestEnsureBucket(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()

	exists, err := c.BucketExists(ctx)
	if err != nil {
		t.Fatalf("bucket exists: %v", err)
	}
	if !exists {
		t.Fatal("bucket was not created")
	}
	// Повторный вызов при старте второго экземпляра не должен падать.
	if err := c.EnsureBucket(ctx); err != nil {
		t.Errorf("second ensure: %v", err)
	}
}

func TestUploadDownload(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()

	key := domain.OriginalKey(uuid.New(), "png")
	data := []byte("\x89PNG fake image bytes")
	if err := c.Upload(ctx, key, bytes.NewReader(data), int64(len(data)), "image/png"); err != nil {
		t.Fatalf("upload: %v", err)
	}

	obj, err := c.Download(ctx, key)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer obj.Body.Close()

	got, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("body = %q, want %q", got, data)
	}
	if obj.Size != int64(len(data)) {
		t.Errorf("size = %d, want %d", obj.Size, len(data))
	}
	if obj.ContentType != "image/png" {
		t.Errorf("content type = %q, want image/png", obj.ContentType)
	}
	if obj.ETag == "" {
		t.Error("etag is empty")
	}
}

func TestUploadUnknownSize(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()

	key := domain.ThumbnailKey(uuid.New(), domain.ThumbnailSize100)
	if err := c.Upload(ctx, key, strings.NewReader("thumb"), -1, "image/jpeg"); err != nil {
		t.Fatalf("upload: %v", err)
	}

	obj, err := c.Download(ctx, key)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer obj.Body.Close()
	if obj.Size != int64(len("thumb")) {
		t.Errorf("size = %d, want %d", obj.Size, len("thumb"))
	}
}

func TestDownloadMissing(t *testing.T) {
	c := testClient(t)

	_, err := c.Download(context.Background(), domain.OriginalKey(uuid.New(), "jpg"))
	if !errors.Is(err, domain.ErrObjectNotFound) {
		t.Fatalf("err = %v, want domain.ErrObjectNotFound", err)
	}
}

func TestDelete(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()

	key := domain.OriginalKey(uuid.New(), "jpg")
	if err := c.Upload(ctx, key, strings.NewReader("img"), 3, "image/jpeg"); err != nil {
		t.Fatalf("upload: %v", err)
	}

	if err := c.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := c.Download(ctx, key); !errors.Is(err, domain.ErrObjectNotFound) {
		t.Errorf("download after delete: err = %v, want domain.ErrObjectNotFound", err)
	}

	// Повторное удаление и удаление того, чего не было, — не ошибка.
	if err := c.Delete(ctx, key); err != nil {
		t.Errorf("second delete: %v", err)
	}
	if err := c.Delete(ctx, domain.OriginalKey(uuid.New(), "jpg")); err != nil {
		t.Errorf("delete missing: %v", err)
	}
}
