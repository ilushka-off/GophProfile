package services

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"go-avatar-service/internal/domain"
)

// recordSpans подменяет глобальный TracerProvider: сервис берёт tracer из него.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	})
	return rec
}

func onlySpan(t *testing.T, rec *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	ended := rec.Ended()
	if len(ended) != 1 || ended[0].Name() != name {
		t.Fatalf("spans = %d, want one %q", len(ended), name)
	}
	return ended[0]
}

func spanAttrs(span sdktrace.ReadOnlySpan) map[attribute.Key]string {
	m := map[attribute.Key]string{}
	for _, kv := range span.Attributes() {
		m[kv.Key] = kv.Value.String()
	}
	return m
}

func eventNames(span sdktrace.ReadOnlySpan) []string {
	var names []string
	for _, e := range span.Events() {
		names = append(names, e.Name)
	}
	return names
}

func TestUploadSpan(t *testing.T) {
	rec := recordSpans(t)
	d := newDeps()
	data := encodePNG(t, 64, 48)

	a, err := d.svc.Upload(context.Background(), uploadInput(data))
	if err != nil {
		t.Fatal(err)
	}

	span := onlySpan(t, rec, "upload_avatar")
	attrs := spanAttrs(span)
	want := map[attribute.Key]string{
		"user.id":   "user-42",
		"avatar.id": a.ID.String(),
		"mime.type": "image/png",
	}
	for k, v := range want {
		if attrs[k] != v {
			t.Errorf("%s = %q, want %q", k, attrs[k], v)
		}
	}
	if attrs["file.size"] == "" {
		t.Error("file.size is not set")
	}
	if got := eventNames(span); len(got) != 3 || got[0] != "validated" || got[1] != "stored" || got[2] != "published" {
		t.Errorf("events = %v, want [validated stored published]", got)
	}
	if span.Status().Code == codes.Error {
		t.Error("successful upload is marked as error")
	}
}

func TestUploadSpanOutcome(t *testing.T) {
	t.Run("invalid file is rejected, not an error", func(t *testing.T) {
		rec := recordSpans(t)
		d := newDeps()

		_, err := d.svc.Upload(context.Background(), uploadInput([]byte("not an image")))
		if !errors.Is(err, domain.ErrInvalidFormat) {
			t.Fatalf("err = %v", err)
		}
		span := onlySpan(t, rec, "upload_avatar")
		if span.Status().Code == codes.Error {
			t.Error("client error marks the span as error")
		}
		if got := eventNames(span); len(got) != 1 || got[0] != "rejected" {
			t.Errorf("events = %v, want [rejected]", got)
		}
	})

	t.Run("broker failure is an error", func(t *testing.T) {
		rec := recordSpans(t)
		d := newDeps()
		d.publisher.err = errors.New("broker is down")

		if _, err := d.svc.Upload(context.Background(), uploadInput(encodePNG(t, 8, 8))); err == nil {
			t.Fatal("err = nil")
		}
		span := onlySpan(t, rec, "upload_avatar")
		if span.Status().Code != codes.Error {
			t.Errorf("status = %v, want error", span.Status())
		}
	})
}

func TestDeleteSpan(t *testing.T) {
	rec := recordSpans(t)
	d := newDeps()
	d.repo.byID = ownedAvatar("user-42")

	if err := d.svc.DeleteCurrent(context.Background(), "user-42", "user-42"); err != nil {
		t.Fatal(err)
	}
	span := onlySpan(t, rec, "delete_avatar")
	if attrs := spanAttrs(span); attrs["user.id"] != "user-42" || attrs["avatar.id"] != testAvatarID.String() {
		t.Errorf("attributes = %v", attrs)
	}

	rec = recordSpans(t)
	if err := d.svc.Delete(context.Background(), "intruder", testAvatarID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v", err)
	}
	if span := onlySpan(t, rec, "delete_avatar"); span.Status().Code == codes.Error {
		t.Error("forbidden delete marks the span as error")
	}
}
