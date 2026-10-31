package handlers

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/middleware"
	"go-avatar-service/internal/services"
	"go-avatar-service/internal/storage"
)

// memRepo — репозиторий в памяти: в этом тесте важны спаны, а не SQL.
type memRepo struct {
	services.AvatarRepository
	created *domain.Avatar
}

func (r *memRepo) Create(_ context.Context, a *domain.Avatar) error {
	r.created = a
	return nil
}

// memStorage — хранилище в памяти.
type memStorage struct{ files map[string][]byte }

func (s *memStorage) Upload(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	b, err := io.ReadAll(r)
	s.files[key] = b
	return err
}

func (s *memStorage) Download(context.Context, string) (*domain.Object, error) {
	return nil, domain.ErrObjectNotFound
}
func (s *memStorage) Delete(context.Context, string) error       { return nil }
func (s *memStorage) BucketExists(context.Context) (bool, error) { return true, nil }

// Загрузка через настоящий роутер, сервис и обёртки трейсинга даёт один
// трейс: HTTP-спан → upload_avatar → s3.PutObject и publish, а traceparent
// уходит в заголовках события и в ответе как X-Trace-ID.
func TestUploadTraceTree(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	mem := &memStorage{files: map[string][]byte{}}
	published := &recordingPublisher{}
	svc, err := services.NewAvatarService(&memRepo{},
		storage.NewTraced(mem, "avatars", tp),
		broker.TracePublisher(published, broker.SystemRabbitMQ, broker.ExchangeAvatars, tp),
		testMaxUploadSize)
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(discardLogger(), RouterOptions{},
		healthyHandler(),
		NewAvatarHandler(svc, testMaxUploadSize, discardLogger()),
		newTestWebHandler(&fakeAvatarService{}),
	)

	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	body, contentType := multipartBody(t, formFieldFile, "me.png", img.Bytes())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/avatars", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(headerUserID, "user-42")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", resp.Code, resp.Body)
	}

	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range rec.Ended() {
		byName[s.Name()] = s
	}
	parentOf := func(child, parent string) {
		t.Helper()
		c, p := byName[child], byName[parent]
		if c == nil || p == nil {
			t.Fatalf("no span %q or %q among %v", child, parent, keys(byName))
		}
		if c.Parent().SpanID() != p.SpanContext().SpanID() {
			t.Errorf("%s is not a child of %s", child, parent)
		}
	}
	parentOf("upload_avatar", "POST /api/v1/avatars")
	parentOf("s3.PutObject", "upload_avatar")
	parentOf("publish avatars.exchange", "upload_avatar")

	traceID := byName["POST /api/v1/avatars"].SpanContext().TraceID().String()
	if got := resp.Header().Get(middleware.HeaderTraceID); got != traceID {
		t.Errorf("%s = %q, want %q", middleware.HeaderTraceID, got, traceID)
	}
	if len(published.msgs) != 1 {
		t.Fatalf("published %d messages", len(published.msgs))
	}
	carrier := propagation.MapCarrier(published.msgs[0].Headers)
	if got := otel.GetTextMapPropagator().Extract(context.Background(), carrier); !sameTrace(got, traceID) {
		t.Errorf("event headers %v do not continue trace %s", published.msgs[0].Headers, traceID)
	}
	if _, err := uuid.Parse(published.msgs[0].Key); err != nil {
		t.Errorf("event key = %q", published.msgs[0].Key)
	}
}

type recordingPublisher struct{ msgs []broker.Message }

func (p *recordingPublisher) Publish(_ context.Context, msg broker.Message) error {
	p.msgs = append(p.msgs, msg)
	return nil
}
func (p *recordingPublisher) Ping(context.Context) error { return nil }
func (p *recordingPublisher) Close() error               { return nil }

func sameTrace(ctx context.Context, traceID string) bool {
	return trace.SpanContextFromContext(ctx).TraceID().String() == traceID
}

func keys(m map[string]sdktrace.ReadOnlySpan) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
