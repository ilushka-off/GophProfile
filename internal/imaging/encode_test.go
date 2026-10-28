package imaging

import (
	"bytes"
	"image"
	"image/color"
	"net/http"
	"testing"

	_ "golang.org/x/image/webp" // декодер для image.DecodeConfig
)

func TestEncode(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})

	for _, mimeType := range []string{"image/jpeg", "image/png", "image/webp"} {
		t.Run(mimeType, func(t *testing.T) {
			var buf bytes.Buffer
			if err := Encode(&buf, img, mimeType); err != nil {
				t.Fatalf("encode: %v", err)
			}
			if got := http.DetectContentType(buf.Bytes()); got != mimeType {
				t.Errorf("detected %q, want %q", got, mimeType)
			}
			cfg, _, err := image.DecodeConfig(&buf)
			if err != nil {
				t.Fatalf("decode config: %v", err)
			}
			if cfg.Width != 8 || cfg.Height != 6 {
				t.Errorf("size = %dx%d, want 8x6", cfg.Width, cfg.Height)
			}
		})
	}
}

func TestEncodeJPEGTransparentIsWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8)) // целиком прозрачная

	var buf bytes.Buffer
	if err := Encode(&buf, img, "image/jpeg"); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, _, err := image.Decode(&buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r, g, b, _ := got.At(4, 4).RGBA(); r>>8 < 250 || g>>8 < 250 || b>>8 < 250 {
		t.Errorf("pixel = %d,%d,%d; want white", r>>8, g>>8, b>>8)
	}
}

func TestEncodeUnsupported(t *testing.T) {
	var buf bytes.Buffer
	if err := Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1)), "image/gif"); err == nil {
		t.Fatal("expected error for image/gif")
	}
}
