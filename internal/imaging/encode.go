// Package imaging кодирует картинки в поддерживаемые сервисом форматы.
package imaging

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"

	"github.com/HugoSmits86/nativewebp"
)

// jpegQuality — качество JPEG: заметно лучше стандартных 75, а файл ненамного больше.
const jpegQuality = 90

// Encode пишет img в w в формате mimeType: image/jpeg, image/png или image/webp.
// WebP всегда lossless: чистый Go-кодировщик без cgo умеет только так.
func Encode(w io.Writer, img image.Image, mimeType string) error {
	switch mimeType {
	case "image/jpeg":
		return jpeg.Encode(w, flatten(img), &jpeg.Options{Quality: jpegQuality})
	case "image/png":
		return png.Encode(w, img)
	case "image/webp":
		return nativewebp.Encode(w, img, nil)
	default:
		return fmt.Errorf("encode: unsupported format %q", mimeType)
	}
}

// flatten заливает прозрачные области белым, как в Thumbnail: в JPEG нет
// альфа-канала, и без заливки прозрачный PNG или WebP стал бы чёрным.
// Непрозрачную картинку возвращает как есть.
func flatten(img image.Image) image.Image {
	if o, ok := img.(interface{ Opaque() bool }); ok && o.Opaque() {
		return img
	}
	b := img.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(dst, b, img, b.Min, draw.Over)
	return dst
}
