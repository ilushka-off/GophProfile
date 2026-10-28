package imaging

import (
	"image"
	"image/color"

	"golang.org/x/image/draw"
)

// Thumbnail делает квадратную миниатюру side×side: вырезает из центра
// наибольший квадрат и масштабирует его (CatmullRom — заметно чище, чем
// билинейный, на сильном уменьшении). Прозрачные области заливаются белым:
// миниатюры сохраняются в JPEG, где альфа-канала нет, и без заливки
// прозрачный PNG стал бы чёрным. Исходную картинку не меняет.
func Thumbnail(src image.Image, side int) *image.RGBA {
	b := src.Bounds()
	crop := min(b.Dx(), b.Dy())
	x0 := b.Min.X + (b.Dx()-crop)/2
	y0 := b.Min.Y + (b.Dy()-crop)/2
	square := image.Rect(x0, y0, x0+crop, y0+crop)

	dst := image.NewRGBA(image.Rect(0, 0, side, side))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, square, draw.Over, nil)
	return dst
}
