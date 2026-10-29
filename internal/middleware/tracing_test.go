package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// recordSpans подменяет глобальный TracerProvider на записывающий спаны в память.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	return rec
}

func newTracedRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(Tracing("/health"))
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/avatars/{avatar_id}", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		r.Get("/users/{user_id}/avatars", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})
	return r
}

func TestTracingNamesSpanByRoute(t *testing.T) {
	spans := recordSpans(t)

	rec := httptest.NewRecorder()
	newTracedRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/123", nil))

	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("got %d spans, want 1", len(ended))
	}
	span := ended[0]
	if want := "GET /api/v1/avatars/{avatar_id}"; span.Name() != want {
		t.Errorf("span name = %q, want %q", span.Name(), want)
	}
	if got := attrValue(span, semconv.HTTPRouteKey); got != "/api/v1/avatars/{avatar_id}" {
		t.Errorf("http.route = %q", got)
	}
	if got := attrValue(span, semconv.HTTPResponseStatusCodeKey); got != "500" {
		t.Errorf("http.response.status_code = %q, want 500", got)
	}
	if span.Status().Code != codes.Error {
		t.Errorf("status = %v, want Error for 5xx", span.Status().Code)
	}
	if got, want := rec.Header().Get(HeaderTraceID), span.SpanContext().TraceID().String(); got != want {
		t.Errorf("%s = %q, want %q", HeaderTraceID, got, want)
	}
}

func TestTracingContinuesIncomingTrace(t *testing.T) {
	spans := recordSpans(t)
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/u1/avatars", nil)
	req.Header.Set("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
	rec := httptest.NewRecorder()
	newTracedRouter().ServeHTTP(rec, req)

	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("got %d spans, want 1", len(ended))
	}
	if got := ended[0].SpanContext().TraceID().String(); got != traceID {
		t.Errorf("trace id = %s, want %s from traceparent", got, traceID)
	}
	if ended[0].Status().Code == codes.Error {
		t.Error("2xx span is marked as error")
	}
	if rec.Header().Get(HeaderTraceID) != traceID {
		t.Errorf("%s = %q, want %s", HeaderTraceID, rec.Header().Get(HeaderTraceID), traceID)
	}
}

func TestTracingSkipsServicePaths(t *testing.T) {
	spans := recordSpans(t)

	rec := httptest.NewRecorder()
	newTracedRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if n := len(spans.Ended()); n != 0 {
		t.Errorf("got %d spans for /health, want 0", n)
	}
	if got := rec.Header().Get(HeaderTraceID); got != "" {
		t.Errorf("%s = %q for an untraced path", HeaderTraceID, got)
	}
}

// attrValue возвращает значение атрибута спана строкой или "", если его нет.
func attrValue(span sdktrace.ReadOnlySpan, key attribute.Key) string {
	for _, kv := range span.Attributes() {
		if kv.Key == key {
			return kv.Value.Emit()
		}
	}
	return ""
}
