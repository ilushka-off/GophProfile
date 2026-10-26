package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/services"
)

var _ services.Storage = (*Client)(nil)

// Интеграционные тесты идут на настоящий S3: на тот, что задан в TEST_S3_*
// (например, SeaweedFS из docker compose), а если он не задан — на MinIO
// в контейнере, общем для всех тестов пакета. Каждый тест работает в своём
// новом бакете и удаляет его за собой.
const (
	endpointEnv  = "TEST_S3_ENDPOINT"
	accessKeyEnv = "TEST_S3_ACCESS_KEY"
	secretKeyEnv = "TEST_S3_SECRET_KEY"

	// Тот же SeaweedFS, что в docker compose: образы MinIO больше не публикуются на Docker Hub.
	seaweedImage  = "chrislusf/seaweedfs:4.48"
	testAccessKey = "test"
	testSecretKey = "test-secret"
	s3Port        = "8333/tcp"
)

var (
	s3Once      sync.Once
	s3Container *testcontainers.DockerContainer
	s3Err       error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if s3Container != nil {
		_ = testcontainers.TerminateContainer(s3Container)
	}
	os.Exit(code)
}

// s3Endpoint возвращает адрес и ключи S3 для тестов или пропускает тест.
func s3Endpoint(t *testing.T) (endpoint, accessKey, secretKey string) {
	t.Helper()

	if endpoint := os.Getenv(endpointEnv); endpoint != "" {
		return endpoint, os.Getenv(accessKeyEnv), os.Getenv(secretKeyEnv)
	}
	if testing.Short() {
		t.Skip("integration test, skipped with -short")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)

	s3Once.Do(func() {
		s3Container, s3Err = startSeaweedFS()
	})
	if s3Err != nil {
		t.Fatalf("start seaweedfs: %v", s3Err)
	}
	endpoint, err := s3Container.PortEndpoint(context.Background(), s3Port, "")
	if err != nil {
		t.Fatalf("seaweedfs endpoint: %v", err)
	}
	return endpoint, testAccessKey, testSecretKey
}

// startSeaweedFS поднимает SeaweedFS с S3 API и одним пользователем-админом,
// как в docker compose.
func startSeaweedFS() (*testcontainers.DockerContainer, error) {
	identities := `{"identities":[{"name":"test","credentials":[{"accessKey":"` + testAccessKey +
		`","secretKey":"` + testSecretKey + `"}],"actions":["Admin","Read","Write","List","Tagging"]}]}`

	return testcontainers.Run(context.Background(), seaweedImage,
		testcontainers.WithEntrypoint("weed"),
		testcontainers.WithCmd("server", "-dir=/data", "-ip.bind=0.0.0.0", "-s3", "-s3.port=8333", "-s3.config=/etc/s3.json"),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			Reader:            strings.NewReader(identities),
			ContainerFilePath: "/etc/s3.json",
			FileMode:          0o644,
		}),
		testcontainers.WithExposedPorts(s3Port),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/healthz").WithPort(s3Port)),
	)
}

func testClient(t *testing.T) *Client {
	t.Helper()

	endpoint, accessKey, secretKey := s3Endpoint(t)
	c, err := New(config.S3Config{
		Endpoint:  endpoint,
		Region:    "us-east-1",
		AccessKey: accessKey,
		SecretKey: secretKey,
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
