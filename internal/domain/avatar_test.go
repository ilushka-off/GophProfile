package domain

import "testing"

func TestAvatarAPIStatus(t *testing.T) {
	tests := []struct {
		status ProcessingStatus
		want   string
	}{
		{ProcessingStatusPending, APIStatusProcessing},
		{ProcessingStatusProcessing, APIStatusProcessing},
		{ProcessingStatusCompleted, APIStatusReady},
		{ProcessingStatusFailed, APIStatusFailed},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			a := Avatar{ProcessingStatus: tt.status}
			if got := a.APIStatus(); got != tt.want {
				t.Errorf("APIStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}
