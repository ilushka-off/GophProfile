package worker

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/imaging"
)

// encoded кодирует картинку w×h в mimeType через imaging.Encode.
func encoded(t *testing.T, img image.Image, mimeType string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := imaging.Encode(&buf, img, mimeType); err != nil {
		t.Fatalf("encode %s: %v", mimeType, err)
	}
	return buf.Bytes()
}

func filled(w, h int, c color.Color) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	return img
}

// Миниатюры получаются из любого поддерживаемого формата, а битые файлы —
// постоянная ошибка без повторов.
func TestProcessorSourceFormats(t *testing.T) {
	red := color.NRGBA{R: 255, A: 255}
	validJPEG := encoded(t, filled(800, 600, red), "image/jpeg")

	tests := []struct {
		name    string
		data    []byte
		wantErr error
		wantRGB color.RGBA // цвет в центре миниатюры
	}{
		{name: "jpeg", data: validJPEG, wantRGB: color.RGBA{R: 255, A: 255}},
		{name: "png", data: encoded(t, filled(300, 900, red), "image/png"), wantRGB: color.RGBA{R: 255, A: 255}},
		{name: "webp", data: encoded(t, filled(640, 480, red), "image/webp"), wantRGB: color.RGBA{R: 255, A: 255}},
		{name: "tiny png is upscaled", data: encoded(t, filled(10, 10, red), "image/png"), wantRGB: color.RGBA{R: 255, A: 255}},
		{
			name:    "transparent png becomes white",
			data:    encoded(t, filled(200, 200, color.NRGBA{}), "image/png"),
			wantRGB: color.RGBA{R: 255, G: 255, B: 255, A: 255},
		},
		{name: "truncated jpeg", data: validJPEG[:len(validJPEG)/3], wantErr: ErrBrokenImage},
		{name: "empty file", data: nil, wantErr: ErrBrokenImage},
		{name: "not an image", data: []byte("hello, world"), wantErr: ErrBrokenImage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, repo, storage := setup(t)
			storage.objects[repo.avatar.S3Key] = tt.data

			err := p.Handle(context.Background(), uploadedMessage(t))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				if len(storage.uploaded) != 0 {
					t.Errorf("thumbnails were uploaded for a broken file")
				}
				return
			}
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}

			thumb, err := jpeg.Decode(bytes.NewReader(storage.uploaded[domain.ThumbnailKey(testID, domain.ThumbnailSize100)]))
			if err != nil {
				t.Fatalf("decode thumbnail: %v", err)
			}
			if b := thumb.Bounds(); b.Dx() != 100 || b.Dy() != 100 {
				t.Errorf("thumbnail is %v, want 100x100", b)
			}
			r, g, b, _ := thumb.At(50, 50).RGBA()
			got := color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 255}
			if !near(got, tt.wantRGB) {
				t.Errorf("center pixel = %v, want about %v", got, tt.wantRGB)
			}
		})
	}
}

// near сравнивает цвета с допуском на потери JPEG.
func near(a, b color.RGBA) bool {
	diff := func(x, y uint8) int {
		if x > y {
			return int(x - y)
		}
		return int(y - x)
	}
	const tolerance = 12
	return diff(a.R, b.R) <= tolerance && diff(a.G, b.G) <= tolerance && diff(a.B, b.B) <= tolerance
}
