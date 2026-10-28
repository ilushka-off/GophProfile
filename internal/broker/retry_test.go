package broker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

var testDelays = []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// countingHandler возвращает ошибки из errs по очереди, потом nil.
func countingHandler(calls *int, errs ...error) Handler {
	return func(context.Context, Message) error {
		*calls++
		if *calls <= len(errs) {
			return errs[*calls-1]
		}
		return nil
	}
}

func TestRetryable(t *testing.T) {
	base := errors.New("s3 timeout")
	err := Retryable(base)

	if !errors.Is(err, ErrRetryable) || !errors.Is(err, base) {
		t.Errorf("Retryable(%v) must match both ErrRetryable and the cause", base)
	}
	if err.Error() != base.Error() {
		t.Errorf("Error() = %q, want %q", err.Error(), base.Error())
	}
	if Retryable(nil) != nil {
		t.Error("Retryable(nil) must be nil")
	}
}

func TestWithRetry(t *testing.T) {
	temporary := Retryable(errors.New("db down"))
	permanent := errors.New("broken image")

	tests := []struct {
		name      string
		errs      []error
		wantErr   error
		wantCalls int
	}{
		{"success first time", nil, nil, 1},
		{"recovers after two retries", []error{temporary, temporary}, nil, 3},
		{"gives up after three retries", []error{temporary, temporary, temporary, temporary}, ErrRetryable, 4},
		{"permanent error is not retried", []error{permanent}, permanent, 1},
		{"permanent after temporary stops", []error{temporary, permanent}, permanent, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			h := WithRetry(countingHandler(&calls, tt.errs...), testDelays, discardLogger())

			err := h(context.Background(), Message{ID: "m1"})
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if calls != tt.wantCalls {
				t.Errorf("calls = %d, want %d", calls, tt.wantCalls)
			}
		})
	}
}

func TestWithRetryBackoff(t *testing.T) {
	var calls int
	var at []time.Time
	h := WithRetry(func(context.Context, Message) error {
		calls++
		at = append(at, time.Now())
		return Retryable(errors.New("down"))
	}, []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}, discardLogger())

	_ = h(context.Background(), Message{})

	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	for i, want := range []time.Duration{10 * time.Millisecond, 20 * time.Millisecond} {
		if got := at[i+1].Sub(at[i]); got < want {
			t.Errorf("delay before retry %d = %v, want at least %v", i+1, got, want)
		}
	}
}

func TestWithRetryStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls int
	h := WithRetry(func(context.Context, Message) error {
		calls++
		cancel()
		return Retryable(errors.New("down"))
	}, []time.Duration{time.Hour}, discardLogger())

	if err := h(ctx, Message{}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}
