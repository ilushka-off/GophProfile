package observability

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

var errClient = errors.New("bad input")

func isClient(err error) bool { return errors.Is(err, errClient) }

func TestEndSpan(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus codes.Code
		wantEvent  string
	}{
		{name: "success", err: nil, wantStatus: codes.Unset},
		{name: "client error", err: errClient, wantStatus: codes.Unset, wantEvent: "rejected"},
		{name: "failure", err: errors.New("db is down"), wantStatus: codes.Error, wantEvent: "exception"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
			_, span := tp.Tracer("test").Start(context.Background(), "op")

			EndSpan(span, tt.err, isClient)

			ended := rec.Ended()
			if len(ended) != 1 {
				t.Fatalf("span is not ended")
			}
			if got := ended[0].Status().Code; got != tt.wantStatus {
				t.Errorf("status = %v, want %v", got, tt.wantStatus)
			}
			events := ended[0].Events()
			switch {
			case tt.wantEvent == "" && len(events) != 0:
				t.Errorf("events = %v, want none", events)
			case tt.wantEvent != "" && (len(events) != 1 || events[0].Name != tt.wantEvent):
				t.Errorf("events = %v, want %s", events, tt.wantEvent)
			}
		})
	}
}
