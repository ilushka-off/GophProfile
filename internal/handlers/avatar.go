package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/httpx"
	"go-avatar-service/internal/services"
)

const (
	headerUserID    = "X-User-ID"
	maxUserIDLength = 255

	formFieldFile = "file"

	// multipartOverhead — запас к лимиту файла на заголовки и границы multipart:
	// тело запроса всегда чуть больше самого файла.
	multipartOverhead = 1 << 20 // 1 MiB
	// multipartMemory — сколько формы держать в памяти, остальное уходит во временные файлы.
	multipartMemory = 1 << 20 // 1 MiB
)

// AvatarService — операции с аватарками, которые нужны хендлеру.
type AvatarService interface {
	Upload(ctx context.Context, in services.UploadInput) (*domain.Avatar, error)
}

// avatarResponse — модель Avatar из API: ответ на загрузку и элемент списка.
type avatarResponse struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	URL       string    `json:"url"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func newAvatarResponse(a *domain.Avatar) avatarResponse {
	return avatarResponse{
		ID:        a.ID.String(),
		UserID:    a.UserID,
		URL:       "/api/v1/avatars/" + a.ID.String(),
		Status:    a.APIStatus(),
		CreatedAt: a.CreatedAt.UTC(),
	}
}

// AvatarHandler обслуживает эндпоинты /api/v1/avatars.
type AvatarHandler struct {
	svc           AvatarService
	maxUploadSize int64
	logger        *slog.Logger
}

// NewAvatarHandler создаёт AvatarHandler. maxUploadSize — лимит размера файла в байтах.
func NewAvatarHandler(svc AvatarService, maxUploadSize int64, logger *slog.Logger) *AvatarHandler {
	return &AvatarHandler{svc: svc, maxUploadSize: maxUploadSize, logger: logger}
}

// Upload — POST /api/v1/avatars: принимает файл из поля формы file
// и отвечает 201 со статусом processing, не дожидаясь миниатюр.
func (h *AvatarHandler) Upload(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// Лимит ставится до чтения тела, чтобы большой файл не попал ни в память, ни на диск.
	r.Body = http.MaxBytesReader(w, r.Body, h.maxUploadSize+multipartOverhead)
	if err := r.ParseMultipartForm(multipartMemory); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeTooLarge(w, h.maxUploadSize)
			return
		}
		httpx.WriteError(w, http.StatusBadRequest, msgInvalidRequest, detailsNotMultipart)
		return
	}
	defer func() {
		if err := r.MultipartForm.RemoveAll(); err != nil {
			h.logger.WarnContext(r.Context(), "remove multipart temp files", "err", err)
		}
	}()

	file, header, err := r.FormFile(formFieldFile)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, msgInvalidRequest, detailsFileRequired)
		return
	}
	defer file.Close()

	avatar, err := h.svc.Upload(r.Context(), services.UploadInput{
		UserID:   userID,
		FileName: header.Filename,
		File:     file,
		Size:     header.Size,
	})
	if err != nil {
		writeServiceError(w, r, h.logger, err, h.maxUploadSize)
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, newAvatarResponse(avatar))
}

// requireUserID достаёт обязательный X-User-ID. Если его нет или он длиннее
// 255 символов, пишет 400 и возвращает false.
func requireUserID(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID := r.Header.Get(headerUserID)
	switch {
	case userID == "":
		httpx.WriteError(w, http.StatusBadRequest, msgInvalidRequest, detailsUserIDRequired)
		return "", false
	case utf8.RuneCountInString(userID) > maxUserIDLength:
		httpx.WriteError(w, http.StatusBadRequest, msgInvalidRequest, detailsUserIDTooLong)
		return "", false
	}
	return userID, true
}
