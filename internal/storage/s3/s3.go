// Package s3 — клиент S3-совместимого хранилища (SeaweedFS, MinIO) поверх minio-go.
package s3

import (
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/domain"
)

// Client работает с одним бакетом.
type Client struct {
	mc     *minio.Client
	bucket string
	region string
}

// New создаёт клиент по конфигу. Соединение не проверяется: для этого есть EnsureBucket.
func New(cfg config.S3Config) (*Client, error) {
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("create s3 client: %w", err)
	}
	return &Client{mc: mc, bucket: cfg.Bucket, region: cfg.Region}, nil
}

// EnsureBucket создаёт бакет, если его нет. Вызывается при старте.
// Если бакет успел создать другой экземпляр сервиса, это не ошибка.
func (c *Client) EnsureBucket(ctx context.Context) error {
	exists, err := c.BucketExists(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	err = c.mc.MakeBucket(ctx, c.bucket, minio.MakeBucketOptions{Region: c.region})
	if err != nil {
		switch minio.ToErrorResponse(err).Code {
		case minio.BucketAlreadyOwnedByYou, minio.BucketAlreadyExists:
			return nil
		}
		return fmt.Errorf("create bucket %q: %w", c.bucket, err)
	}
	return nil
}

// BucketExists проверяет, что бакет существует. Используется и в /health.
func (c *Client) BucketExists(ctx context.Context) (bool, error) {
	exists, err := c.mc.BucketExists(ctx, c.bucket)
	if err != nil {
		return false, fmt.Errorf("check bucket %q: %w", c.bucket, err)
	}
	return exists, nil
}

// Upload кладёт файл по ключу. size — размер в байтах, -1 если неизвестен
// (тогда minio-go грузит частями и держит часть в памяти).
func (c *Client) Upload(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := c.mc.PutObject(ctx, c.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("upload %q: %w", key, err)
	}
	return nil
}

// Download отдаёт файл потоком, не читая его в память. Body закрывает вызывающий.
// Если файла нет, возвращает domain.ErrObjectNotFound.
func (c *Client) Download(ctx context.Context, key string) (*domain.Object, error) {
	obj, err := c.mc.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("download %q: %w", key, err)
	}

	// GetObject ленивый: запрос уходит только при первом чтении или Stat,
	// поэтому отсутствие файла узнаём здесь, а не посреди отдачи ответа.
	info, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		if minio.ToErrorResponse(err).Code == minio.NoSuchKey {
			return nil, fmt.Errorf("download %q: %w", key, domain.ErrObjectNotFound)
		}
		return nil, fmt.Errorf("download %q: %w", key, err)
	}

	return &domain.Object{
		Body:        obj,
		Size:        info.Size,
		ContentType: info.ContentType,
		ETag:        info.ETag,
	}, nil
}

// Delete удаляет файл. Отсутствующий файл — не ошибка: worker удаляет идемпотентно
// и может получить одно и то же событие повторно.
func (c *Client) Delete(ctx context.Context, key string) error {
	err := c.mc.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{})
	if err != nil && minio.ToErrorResponse(err).Code != minio.NoSuchKey {
		return fmt.Errorf("delete %q: %w", key, err)
	}
	return nil
}
