package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"go-avatar-service/internal/broker"
)

func TestFinishInFlightCompletesAfterStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})

	h := FinishInFlight(func(hctx context.Context, _ broker.Message) error {
		close(started)
		// Обработка длится дольше, чем до сигнала остановки.
		select {
		case <-time.After(50 * time.Millisecond):
			return nil
		case <-hctx.Done():
			return hctx.Err()
		}
	}, time.Second)

	done := make(chan error, 1)
	go func() { done <- h(ctx, broker.Message{ID: "m1"}) }()
	<-started
	cancel()

	if err := <-done; err != nil {
		t.Errorf("handler error = %v, want nil: in-flight message must finish after stop", err)
	}
}

func TestFinishInFlightCancelsAfterDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})

	h := FinishInFlight(func(hctx context.Context, _ broker.Message) error {
		close(started)
		<-hctx.Done()
		return hctx.Err()
	}, 20*time.Millisecond)

	done := make(chan error, 1)
	go func() { done <- h(ctx, broker.Message{ID: "m1"}) }()
	<-started
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("handler error = %v, want context.Canceled after drain", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not canceled after drain timeout")
	}
}

func TestFinishInFlightNoStop(t *testing.T) {
	h := FinishInFlight(func(hctx context.Context, _ broker.Message) error {
		if hctx.Err() != nil {
			return hctx.Err()
		}
		return nil
	}, time.Millisecond)

	if err := h(context.Background(), broker.Message{}); err != nil {
		t.Errorf("handler error = %v, want nil", err)
	}
}
