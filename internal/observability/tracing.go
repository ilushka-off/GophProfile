// Package observability — общая для server и worker настройка телеметрии:
// трейсы OpenTelemetry с экспортом по OTLP, JSON-логи с trace_id и реестр
// метрик Prometheus.
package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"go-avatar-service/internal/config"
)

// Имена сервисов в телеметрии: service.name в трейсах и поле service в логах.
const (
	ServiceServer = "avatar-service"
	ServiceWorker = "avatar-worker"
)

// Service — кто пишет телеметрию.
type Service struct {
	Name    string // ServiceServer или ServiceWorker
	Version string // версия сборки, "dev" при локальном запуске
}

// Setup настраивает глобальные TracerProvider и propagator (W3C TraceContext
// и Baggage) и возвращает функцию, которая дописывает накопленные спаны
// и останавливает экспорт; её нужно вызвать при остановке сервиса.
//
// Экспортёр подключается к коллектору лениво и в фоне: недоступный Jaeger
// не мешает старту, спаны просто теряются, а ошибки экспорта пишутся в лог.
func Setup(ctx context.Context, cfg config.ObservabilityConfig, svc Service) (shutdown func(context.Context) error, err error) {
	res, err := newResource(ctx, cfg, svc)
	if err != nil {
		return nil, err
	}

	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.TracesSampleRatio))),
	}
	if cfg.OTLPEndpoint != "" {
		exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(endpointURL(cfg.OTLPEndpoint)))
		if err != nil {
			return nil, fmt.Errorf("create otlp exporter: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exporter))
	}

	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Warn("opentelemetry", "err", err)
	}))

	return tp.Shutdown, nil
}

func newResource(ctx context.Context, cfg config.ObservabilityConfig, svc Service) (*resource.Resource, error) {
	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithAttributes(
			semconv.ServiceName(svc.Name),
			semconv.ServiceVersion(svc.Version),
			semconv.DeploymentEnvironmentNameKey.String(cfg.Environment),
		),
	)
	// Частичный resource (например, не удалось узнать имя хоста) — не повод не стартовать.
	if err != nil && !errors.Is(err, resource.ErrPartialResource) {
		return nil, fmt.Errorf("create otel resource: %w", err)
	}
	return res, nil
}

// endpointURL приводит endpoint к URL: в OTEL_EXPORTER_OTLP_ENDPOINT обычно
// пишут http://jaeger:4317, но удобно разрешить и просто jaeger:4317.
// Схема http означает соединение без TLS.
func endpointURL(endpoint string) string {
	if strings.Contains(endpoint, "://") {
		return endpoint
	}
	return "http://" + endpoint
}
