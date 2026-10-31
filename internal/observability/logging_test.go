package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func decodeLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("log line is not JSON: %v\n%s", err, buf.String())
	}
	buf.Reset()
	return entry
}

func TestLoggerAddsServiceAndTrace(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo, Service{Name: ServiceServer, Version: "1.0.0"})

	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	logger.InfoContext(ctx, "inside span", "avatar_id", "42")
	entry := decodeLine(t, &buf)

	want := map[string]string{
		"level":       "INFO",
		LogKeyService: ServiceServer,
		LogKeyVersion: "1.0.0",
		LogKeyTraceID: span.SpanContext().TraceID().String(),
		LogKeySpanID:  span.SpanContext().SpanID().String(),
		"avatar_id":   "42",
	}
	for k, v := range want {
		if entry[k] != v {
			t.Errorf("%s = %v, want %q", k, entry[k], v)
		}
	}

	// With не теряет обёртку: trace_id есть и у производного логгера.
	logger.With("component", "x").WarnContext(ctx, "derived")
	if entry := decodeLine(t, &buf); entry[LogKeyTraceID] != want[LogKeyTraceID] {
		t.Errorf("derived logger trace_id = %v, want %q", entry[LogKeyTraceID], want[LogKeyTraceID])
	}
}

func TestLoggerWithoutSpan(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo, Service{Name: ServiceWorker, Version: "dev"})

	logger.InfoContext(context.Background(), "no span")
	entry := decodeLine(t, &buf)
	if _, ok := entry[LogKeyTraceID]; ok {
		t.Errorf("trace_id = %v, want absent without a span", entry[LogKeyTraceID])
	}
	if entry[LogKeyService] != ServiceWorker {
		t.Errorf("service = %v, want %q", entry[LogKeyService], ServiceWorker)
	}
}

func TestLoggerLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelWarn, Service{Name: ServiceServer})

	logger.Info("dropped")
	if buf.Len() != 0 {
		t.Errorf("info is logged at warn level: %s", buf.String())
	}
	logger.Error("kept")
	if entry := decodeLine(t, &buf); entry["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", entry["level"])
	}
}
