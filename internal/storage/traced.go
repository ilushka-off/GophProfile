// Package storage — общие обёртки над файловым хранилищем, не зависящие
// от реализации (internal/storage/s3).
package storage

import (
	"context"
	"io"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"go-avatar-service/internal/domain"
)

// tracerName — имя инструментирования в спанах.
const tracerName = "go-avatar-service/internal/storage"

// Атрибуты спанов S3.
const (
	attrBucket   = attribute.Key("s3.bucket")
	attrKey      = attribute.Key("s3.key")
	attrFileSize = attribute.Key("file.size")
)

// Storage — операции с файлами, которые трейсятся. BucketExists нужен /health.
type Storage interface {
	Upload(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Download(ctx context.Context, key string) (*domain.Object, error)
	Delete(ctx context.Context, key string) error
	BucketExists(ctx context.Context) (bool, error)
}

// Traced — Storage, который пишет спан на каждую операцию с файлом:
// s3.PutObject, s3.GetObject, s3.RemoveObject. Ошибки записываются в спан.
// BucketExists не трейсится: его зовёт только /health.
type Traced struct {
	next   Storage
	bucket string
	tracer trace.Tracer
}

var _ Storage = (*Traced)(nil)

// NewTraced оборачивает next. bucket попадает в атрибут s3.bucket.
func NewTraced(next Storage, bucket string, tp trace.TracerProvider) *Traced {
	return &Traced{next: next, bucket: bucket, tracer: tp.Tracer(tracerName)}
}

func (t *Traced) Upload(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	ctx, span := t.start(ctx, "s3.PutObject", key)
	defer span.End()
	if size >= 0 {
		span.SetAttributes(attrFileSize.Int64(size))
	}

	err := t.next.Upload(ctx, key, r, size, contentType)
	recordError(span, err)
	return err
}

// Download завершает спан, когда файл найден и начал отдаваться: чтение Body
// идёт уже после — в хендлере, пока ответ уходит клиенту.
func (t *Traced) Download(ctx context.Context, key string) (*domain.Object, error) {
	ctx, span := t.start(ctx, "s3.GetObject", key)
	defer span.End()

	obj, err := t.next.Download(ctx, key)
	if err != nil {
		recordError(span, err)
		return nil, err
	}
	if obj.Size >= 0 {
		span.SetAttributes(attrFileSize.Int64(obj.Size))
	}
	return obj, nil
}

func (t *Traced) Delete(ctx context.Context, key string) error {
	ctx, span := t.start(ctx, "s3.RemoveObject", key)
	defer span.End()

	err := t.next.Delete(ctx, key)
	recordError(span, err)
	return err
}

func (t *Traced) BucketExists(ctx context.Context) (bool, error) {
	return t.next.BucketExists(ctx)
}

func (t *Traced) start(ctx context.Context, name, key string) (context.Context, trace.Span) {
	return t.tracer.Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrBucket.String(t.bucket), attrKey.String(key)),
	)
}

// recordError помечает спан ошибкой. nil ничего не меняет.
func recordError(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
