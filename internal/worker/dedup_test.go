package worker

import (
	"context"
	"errors"
	"testing"

	"go-avatar-service/internal/broker"
)

type fakeMessageLog struct {
	processed map[string]bool
	checkErr  error
	markErr   error
}

func (f *fakeMessageLog) IsProcessed(_ context.Context, id string) (bool, error) {
	return f.processed[id], f.checkErr
}

func (f *fakeMessageLog) MarkProcessed(_ context.Context, id, _ string) error {
	if f.markErr != nil {
		return f.markErr
	}
	f.processed[id] = true
	return nil
}

func TestDeduplicate(t *testing.T) {
	errHandler := errors.New("broken image")

	tests := []struct {
		name          string
		log           *fakeMessageLog
		msgID         string
		handlerErr    error
		wantCalls     int
		wantErr       error
		wantProcessed bool
	}{
		{"new message is handled and marked", &fakeMessageLog{processed: map[string]bool{}}, "m1", nil, 1, nil, true},
		{"duplicate is skipped", &fakeMessageLog{processed: map[string]bool{"m1": true}}, "m1", nil, 0, nil, true},
		{"failed message is not marked", &fakeMessageLog{processed: map[string]bool{}}, "m1", errHandler, 1, errHandler, false},
		{"no message id is always handled", &fakeMessageLog{processed: map[string]bool{}}, "", nil, 1, nil, false},
		{"check failure is retryable", &fakeMessageLog{processed: map[string]bool{}, checkErr: errors.New("db down")}, "m1", nil, 0, broker.ErrRetryable, false},
		{"mark failure is only logged", &fakeMessageLog{processed: map[string]bool{}, markErr: errors.New("db down")}, "m1", nil, 1, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			h := Deduplicate(tt.log, discardLogger(), func(context.Context, broker.Message) error {
				calls++
				return tt.handlerErr
			})

			err := h(context.Background(), broker.Message{ID: tt.msgID, Type: broker.TypeAvatarUploaded})
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if calls != tt.wantCalls {
				t.Errorf("handler calls = %d, want %d", calls, tt.wantCalls)
			}
			if got := tt.log.processed[tt.msgID]; got != tt.wantProcessed {
				t.Errorf("processed = %v, want %v", got, tt.wantProcessed)
			}
		})
	}
}
