package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestStorageKeys(t *testing.T) {
	id := uuid.MustParse("0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10")

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"original", OriginalKey(id, "png"), "originals/0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10.png"},
		{"thumbnail 100", ThumbnailKey(id, ThumbnailSize100), "thumbnails/0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10/100x100.jpg"},
		{"thumbnail 300", ThumbnailKey(id, ThumbnailSize300), "thumbnails/0b6c6c9e-5f0e-4a35-9a43-7f3b1c2d9e10/300x300.jpg"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("key = %q, want %q", tt.got, tt.want)
			}
		})
	}
}
