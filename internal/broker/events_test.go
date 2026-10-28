package broker

import (
	"encoding/json"
	"testing"
)

// Формат событий — контракт между server и worker, поэтому JSON проверяется явно.
func TestEventsJSON(t *testing.T) {
	tests := []struct {
		name  string
		event any
		want  string
	}{
		{
			name:  "upload",
			event: AvatarUploadEvent{AvatarID: "a1", UserID: "user-42", S3Key: "originals/a1.png"},
			want:  `{"avatar_id":"a1","user_id":"user-42","s3_key":"originals/a1.png"}`,
		},
		{
			name:  "process",
			event: AvatarProcessEvent{AvatarID: "a1", Operations: []ProcessingOp{{Type: OpThumbnail, Size: "100x100"}}},
			want:  `{"avatar_id":"a1","operations":[{"type":"thumbnail","size":"100x100"}]}`,
		},
		{
			name:  "delete",
			event: AvatarDeleteEvent{AvatarID: "a1", S3Keys: []string{"originals/a1.png"}},
			want:  `{"avatar_id":"a1","s3_keys":["originals/a1.png"]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.event)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("json = %s, want %s", got, tt.want)
			}
		})
	}
}
