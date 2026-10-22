package domain

import "errors"

var (
	ErrNotFound      = errors.New("avatar not found")
	ErrForbidden     = errors.New("forbidden")
	ErrInvalidFormat = errors.New("invalid file format")
	ErrTooLarge      = errors.New("file too large")
)

type ValidationError struct{ Details string }

func (e *ValidationError) Error() string { return e.Details }
