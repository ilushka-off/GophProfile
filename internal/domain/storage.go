package domain

import (
	"io"

	"github.com/google/uuid"
)

// Object — файл, скачиваемый из S3 потоком. Body закрывает вызывающий.
type Object struct {
	Body        io.ReadCloser
	Size        int64
	ContentType string
	ETag        string
}

// Размеры миниатюр. Они же — суффиксы ключей в S3 и значения query-параметра size.
const (
	ThumbnailSize100 = "100x100"
	ThumbnailSize300 = "300x300"
	// SizeOriginal — значение size для оригинала, оно же значение по умолчанию.
	SizeOriginal = "original"
)

// ThumbnailSizes — все миниатюры, которые создаёт worker, от маленькой к большой.
var ThumbnailSizes = []string{ThumbnailSize100, ThumbnailSize300}

// ThumbnailMimeType — формат всех миниатюр: worker сохраняет их в JPEG.
const ThumbnailMimeType = "image/jpeg"

// OriginalKey возвращает ключ оригинала в S3: originals/{id}.{ext}.
func OriginalKey(id uuid.UUID, ext string) string {
	return "originals/" + id.String() + "." + ext
}

// ThumbnailKey возвращает ключ миниатюры в S3: thumbnails/{id}/{size}.jpg.
func ThumbnailKey(id uuid.UUID, size string) string {
	return "thumbnails/" + id.String() + "/" + size + ".jpg"
}
