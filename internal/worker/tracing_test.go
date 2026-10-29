package worker

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestProcessThumbnailsSpan(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	p, _, _ := setup(t)
	if err := p.Handle(context.Background(), uploadedMessage(t)); err != nil {
		t.Fatal(err)
	}

	ended := rec.Ended()
	if len(ended) != 1 || ended[0].Name() != "process_thumbnails" {
		t.Fatalf("spans = %v, want one process_thumbnails", ended)
	}
	span := ended[0]
	attrs := map[string]string{}
	for _, kv := range span.Attributes() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	if attrs["avatar.id"] != testID.String() || attrs["thumbnail.sizes"] != `["100x100","300x300"]` {
		t.Errorf("attributes = %v", attrs)
	}
	var events []string
	for _, e := range span.Events() {
		events = append(events, e.Name)
	}
	if len(events) != 2 || events[0] != "downloaded" || events[1] != "stored" {
		t.Errorf("events = %v, want [downloaded stored]", events)
	}
	if span.Status().Code == codes.Error {
		t.Error("successful processing is marked as error")
	}
}
