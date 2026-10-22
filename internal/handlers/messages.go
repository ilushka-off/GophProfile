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
)

// Тексты ответов роутера на неизвестный маршрут и метод — в ТЗ их нет.
const (
	msgRouteNotFound    = "Not found"
	msgMethodNotAllowed = "Method not allowed"
)
