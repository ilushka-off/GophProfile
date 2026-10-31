package storage

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"go-avatar-service/internal/domain"
)

// fakeStorage возвращает err на каждую операцию с файлом.
type fakeStorage struct {
	err      error
	uploaded string
}

func (f *fakeStorage) Upload(_ context.Context, _ string, r io.Reader, _ int64, _ string) error {
	b, _ := io.ReadAll(r)
	f.uploaded = string(b)
	return f.err
}

func (f *fakeStorage) Download(context.Context, string) (*domain.Object, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &domain.Object{Body: io.NopCloser(strings.NewReader("img")), Size: 3}, nil
}

func (f *fakeStorage) Delete(context.Context, string) error       { return f.err }
func (f *fakeStorage) BucketExists(context.Context) (bool, error) { return true, f.err }

func newTraced(t *testing.T, next Storage) (*Traced, *tracetest.SpanRecorder, trace.Tracer) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return NewTraced(next, "avatars", tp), rec, tp.Tracer("test")
}

func attrs(span sdktrace.ReadOnlySpan) map[attribute.Key]string {
	m := map[attribute.Key]string{}
	for _, kv := range span.Attributes() {
		m[kv.Key] = kv.Value.String()
	}
	return m
}

func TestTracedSpans(t *testing.T) {
	next := &fakeStorage{}
	s, rec, tracer := newTraced(t, next)

	ctx, parent := tracer.Start(context.Background(), "upload_avatar")
	if err := s.Upload(ctx, "originals/1.png", strings.NewReader("data"), 4, "image/png"); err != nil {
		t.Fatal(err)
	}
	obj, err := s.Download(ctx, "originals/1.png")
	if err != nil {
		t.Fatal(err)
	}
	obj.Body.Close()
	if err := s.Delete(ctx, "originals/1.png"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.BucketExists(ctx); !ok || err != nil {
		t.Fatalf("BucketExists() = %v, %v", ok, err)
	}
	parent.End()

	if next.uploaded != "data" {
		t.Errorf("uploaded %q, want the reader to be passed through", next.uploaded)
	}

	ended := rec.Ended()
	wantNames := []string{"s3.PutObject", "s3.GetObject", "s3.RemoveObject", "upload_avatar"}
	if len(ended) != len(wantNames) {
		t.Fatalf("got %d spans, want %d (BucketExists is not traced)", len(ended), len(wantNames))
	}
	for i, span := range ended[:3] {
		if span.Name() != wantNames[i] {
			t.Errorf("span %d name = %q, want %q", i, span.Name(), wantNames[i])
		}
		if span.Parent().SpanID() != parent.SpanContext().SpanID() {
			t.Errorf("%s is not a child of the parent span", span.Name())
		}
		if span.SpanKind() != trace.SpanKindClient {
			t.Errorf("%s kind = %v, want client", span.Name(), span.SpanKind())
		}
		a := attrs(span)
		if a["s3.bucket"] != "avatars" || a["s3.key"] != "originals/1.png" {
			t.Errorf("%s attributes = %v", span.Name(), a)
		}
		if span.Status().Code == codes.Error {
			t.Errorf("%s is marked as error", span.Name())
		}
	}
	if got := attrs(ended[0])["file.size"]; got != "4" {
		t.Errorf("PutObject file.size = %q, want 4", got)
	}
	if got := attrs(ended[1])["file.size"]; got != "3" {
		t.Errorf("GetObject file.size = %q, want 3", got)
	}
}

func TestTracedRecordsErrors(t *testing.T) {
	boom := errors.New("s3 is down")
	s, rec, _ := newTraced(t, &fakeStorage{err: boom})
	ctx := context.Background()

	if err := s.Upload(ctx, "k", strings.NewReader(""), -1, ""); !errors.Is(err, boom) {
		t.Errorf("Upload() error = %v", err)
	}
	if _, err := s.Download(ctx, "k"); !errors.Is(err, boom) {
		t.Errorf("Download() error = %v", err)
	}
	if err := s.Delete(ctx, "k"); !errors.Is(err, boom) {
		t.Errorf("Delete() error = %v", err)
	}

	for _, span := range rec.Ended() {
		if span.Status().Code != codes.Error || span.Status().Description != boom.Error() {
			t.Errorf("%s status = %+v, want error", span.Name(), span.Status())
		}
		if len(span.Events()) == 0 || span.Events()[0].Name != "exception" {
			t.Errorf("%s has no exception event", span.Name())
		}
		if _, ok := attrs(span)["file.size"]; ok {
			t.Errorf("%s has file.size for unknown size", span.Name())
		}
	}
}
