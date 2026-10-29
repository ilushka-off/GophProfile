package observability

import (
	"context"
	"io"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// Поля, которые логгер добавляет к каждой записи. По trace_id лог связывается
// с трейсом в Jaeger, по service — с сервисом в Loki.
const (
	LogKeyService = "service"
	LogKeyVersion = "version"
	LogKeyTraceID = "trace_id"
	LogKeySpanID  = "span_id"
)

// NewLogger создаёт JSON-логгер с полями service и version. Записи,
// сделанные через *Context-методы внутри спана, получают trace_id и span_id.
// Содержимое файлов и секреты (пароли, ключи, строки подключения) в лог
// не пишутся — это правило для вызывающего кода.
func NewLogger(w io.Writer, level slog.Leveler, svc Service) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(traceHandler{Handler: h}).With(
		LogKeyService, svc.Name,
		LogKeyVersion, svc.Version,
	)
}

// traceHandler дописывает в запись trace_id и span_id текущего спана из ctx.
type traceHandler struct {
	slog.Handler
}

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String(LogKeyTraceID, sc.TraceID().String()),
			slog.String(LogKeySpanID, sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{Handler: h.Handler.WithGroup(name)}
}
