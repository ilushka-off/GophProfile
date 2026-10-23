package domain

import "errors"

var (
	ErrNotFound      = errors.New("avatar not found")
	ErrForbidden     = errors.New("forbidden")
	ErrInvalidFormat = errors.New("invalid file format")
	ErrTooLarge      = errors.New("file too large")

	// ErrObjectNotFound — файла нет в S3. Хендлер её не сопоставляет со статусом,
	// поэтому клиент получит 500: строка в БД есть, а файла нет — это сбой, а не 404.
	ErrObjectNotFound = errors.New("object not found")
)

type ValidationError struct{ Details string }

func (e *ValidationError) Error() string { return e.Details }
