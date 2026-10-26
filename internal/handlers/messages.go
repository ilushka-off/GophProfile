package handlers

// Тексты для поля "error" и фиксированные "details" — из ТЗ.
const (
	msgInvalidRequest = "Invalid request"
	msgInvalidFormat  = "Invalid file format"
	msgForbidden      = "Forbidden"
	msgNotFound       = "Avatar not found"
	msgTooLarge       = "File too large"
	msgInternal       = "Internal server error"

	detailsInvalidFormat = "Supported formats: jpeg, png, webp"
	detailsForbidden     = "You can only delete your own avatars"

	detailsUserIDRequired  = "X-User-ID header is required"
	detailsUserIDTooLong   = "X-User-ID must be at most 255 characters"
	detailsFileRequired    = "file is required"
	detailsInvalidAvatarID = "avatar_id must be a valid UUID"
	detailsInvalidSize     = "size: allowed values 100x100, 300x300, original"
	// В ТЗ текст есть только для size, для format — по аналогии.
	detailsInvalidFormatParam = "format: allowed values jpeg, png, webp"
	detailsNotMultipart       = "request body must be multipart/form-data"
)

// Тексты ответов роутера на неизвестный маршрут и метод — в ТЗ их нет.
const (
	msgRouteNotFound    = "Not found"
	msgMethodNotAllowed = "Method not allowed"
	// 429 — бонусная часть ТЗ, текста в нём нет.
	msgTooManyRequests = "Too many requests"
)
