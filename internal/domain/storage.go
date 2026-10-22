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
)

// OriginalKey возвращает ключ оригинала в S3: originals/{id}.{ext}.
func OriginalKey(id uuid.UUID, ext string) string {
	return "originals/" + id.String() + "." + ext
}

// ThumbnailKey возвращает ключ миниатюры в S3: thumbnails/{id}/{size}.jpg.
func ThumbnailKey(id uuid.UUID, size string) string {
	return "thumbnails/" + id.String() + "/" + size + ".jpg"
}
