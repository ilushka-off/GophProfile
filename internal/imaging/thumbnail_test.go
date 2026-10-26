package imaging

import (
	"image"
	"image/color"
	"testing"
)

// halves — картинка w×h: левая половина красная, правая синяя.
func halves(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA{R: 255, A: 255}
			if x >= w/2 {
				c = color.RGBA{B: 255, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func TestThumbnailSize(t *testing.T) {
	tests := []struct {
		name string
		src  image.Image
		side int
	}{
		{"landscape down", halves(1920, 1080), 300},
		{"portrait down", halves(480, 640), 100},
		{"square", halves(500, 500), 100},
		{"upscale small", halves(40, 30), 100},
		{"non-zero origin", halves(800, 600).SubImage(image.Rect(100, 50, 700, 550)), 300},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Thumbnail(tt.src, tt.side)
			if b := got.Bounds(); b.Dx() != tt.side || b.Dy() != tt.side || b.Min != (image.Point{}) {
				t.Errorf("bounds = %v, want 0,0-%d,%d", b, tt.side, tt.side)
			}
		})
	}
}

func TestThumbnailCropsCenter(t *testing.T) {
	// 300×100: красная полоса слева 100px, синий квадрат в центре, красная полоса справа.
	src := image.NewRGBA(image.Rect(0, 0, 300, 100))
	for y := range 100 {
		for x := range 300 {
			c := color.RGBA{R: 255, A: 255}
			if x >= 100 && x < 200 {
				c = color.RGBA{B: 255, A: 255}
			}
			src.SetRGBA(x, y, c)
		}
	}

	got := Thumbnail(src, 50)
	for _, p := range []image.Point{{0, 0}, {25, 25}, {49, 49}} {
		if c := got.RGBAAt(p.X, p.Y); c.R > 10 || c.B < 245 {
			t.Errorf("pixel %v = %v, want blue: the center square must be kept", p, c)
		}
	}
}

func TestThumbnailFlattensTransparency(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 200, 200)) // полностью прозрачная

	got := Thumbnail(src, 100)
	if c := got.RGBAAt(50, 50); c != (color.RGBA{R: 255, G: 255, B: 255, A: 255}) {
		t.Errorf("pixel = %v, want opaque white", c)
	}
}

func TestThumbnailKeepsSource(t *testing.T) {
	src := halves(64, 64)
	before := append([]uint8(nil), src.Pix...)

	Thumbnail(src, 16)

	for i := range before {
		if src.Pix[i] != before[i] {
			t.Fatal("source image was modified")
		}
	}
}
