package observability

import (
	"context"
	"slices"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"go-avatar-service/internal/config"
)

func TestEndpointURL(t *testing.T) {
	tests := map[string]string{
		"jaeger:4317":          "http://jaeger:4317",
		"http://jaeger:4317":   "http://jaeger:4317",
		"https://otel.example": "https://otel.example",
		"localhost:4317":       "http://localhost:4317",
	}
	for in, want := range tests {
		if got := endpointURL(in); got != want {
			t.Errorf("endpointURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// restoreGlobals возвращает глобальные provider и propagator после теста.
func restoreGlobals(t *testing.T) {
	t.Helper()
	tp, prop := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(tp)
		otel.SetTextMapPropagator(prop)
	})
}

func TestSetup(t *testing.T) {
	restoreGlobals(t)

	cfg := config.ObservabilityConfig{
		// Коллектора нет: Setup не должен ни падать, ни ждать подключения.
		OTLPEndpoint:      "127.0.0.1:1",
		TracesSampleRatio: 1,
		Environment:       "test",
	}
	shutdown, err := Setup(context.Background(), cfg, Service{Name: ServiceServer, Version: "1.2.3"})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	tp, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider)
	if !ok {
		t.Fatalf("global tracer provider is %T, want *sdktrace.TracerProvider", otel.GetTracerProvider())
	}
	_, span := tp.Tracer("test").Start(context.Background(), "op")
	if !span.SpanContext().IsSampled() {
		t.Error("span is not sampled with ratio 1")
	}
	ro, ok := span.(sdktrace.ReadOnlySpan)
	if !ok {
		t.Fatalf("span is %T, want sdktrace.ReadOnlySpan", span)
	}
	attrs := ro.Resource().Set()
	for _, want := range []struct{ key, value string }{
		{string(semconv.ServiceNameKey), ServiceServer},
		{string(semconv.ServiceVersionKey), "1.2.3"},
		{string(semconv.DeploymentEnvironmentNameKey), "test"},
	} {
		if v, ok := attrs.Value(attribute.Key(want.key)); !ok || v.AsString() != want.value {
			t.Errorf("resource %s = %q, want %q", want.key, v.AsString(), want.value)
		}
	}
	span.End()

	fields := otel.GetTextMapPropagator().Fields()
	for _, want := range []string{"traceparent", "baggage"} {
		if !slices.Contains(fields, want) {
			t.Errorf("propagator fields = %v, want %q", fields, want)
		}
	}

	// Недоступный коллектор не должен подвешивать остановку дольше её таймаута.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_ = shutdown(ctx)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("shutdown took %v with a 500ms deadline", elapsed)
	}
}

func TestSetupWithoutExporter(t *testing.T) {
	restoreGlobals(t)

	shutdown, err := Setup(context.Background(),
		config.ObservabilityConfig{TracesSampleRatio: 0}, Service{Name: ServiceWorker, Version: "dev"})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	// Ratio 0: новый трейс не записывается, но trace_id у него всё равно есть.
	_, span := otel.Tracer("test").Start(context.Background(), "op")
	defer span.End()
	if span.SpanContext().IsSampled() {
		t.Error("span is sampled with ratio 0")
	}
	if !span.SpanContext().TraceID().IsValid() {
		t.Error("span has no trace id")
	}

	// А продолжение чужого записанного трейса записывается: решает родитель.
	carrier := propagation.MapCarrier{"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}
	ctx := otel.GetTextMapPropagator().Extract(context.Background(), carrier)
	_, child := otel.Tracer("test").Start(ctx, "child")
	defer child.End()
	if !child.SpanContext().IsSampled() {
		t.Error("child of a sampled remote span is not sampled")
	}
}
