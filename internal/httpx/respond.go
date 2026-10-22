// Package httpx содержит общие хелперы HTTP-ответов. Отдельный пакет нужен,
// чтобы единый формат ошибок могли использовать и handlers, и middleware
// без циклического импорта.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// ErrorResponse — единый формат ошибки API: {"error": "...", "details": "..."}.
// Для 413 вместо details передаётся max_size — лимит размера файла в байтах.
type ErrorResponse struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
	MaxSize int64  `json:"max_size,omitempty"`
}

// WriteJSON пишет v как JSON с указанным статусом.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json response", "err", err)
	}
}

// WriteError пишет ошибку в едином формате {"error": msg, "details": details}.
// Пустой details в ответ не попадает.
func WriteError(w http.ResponseWriter, status int, msg, details string) {
	WriteJSON(w, status, ErrorResponse{Error: msg, Details: details})
}
