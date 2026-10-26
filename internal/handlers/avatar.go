package handlers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/httpx"
	"go-avatar-service/internal/services"
)

const (
	headerUserID    = "X-User-ID"
	maxUserIDLength = 255

	urlParamAvatarID = "avatar_id"
	urlParamUserID   = "user_id"

	queryParamSize   = "size"
	queryParamFormat = "format"

	// cacheImmutable — файл по этому URL не меняется, кешируем на сутки.
	cacheImmutable = "max-age=86400"
	// cacheRevalidate — хранить можно, но каждый раз сверять ETag: за тем же URL
	// может оказаться другой файл (новая текущая аватарка, готовая миниатюра).
	cacheRevalidate = "no-cache"

	formFieldFile = "file"

	// multipartOverhead — запас к лимиту файла на заголовки и границы multipart:
	// тело запроса всегда чуть больше самого файла.
	multipartOverhead = 1 << 20 // 1 MiB
	// multipartMemory — сколько формы держать в памяти, остальное уходит во временные файлы.
	multipartMemory = 1 << 20 // 1 MiB
)

// allowedSizes — допустимые значения query-параметра size.
var allowedSizes = map[string]bool{
	domain.ThumbnailSize100: true,
	domain.ThumbnailSize300: true,
	domain.SizeOriginal:     true,
}

// formatMimeTypes — допустимые значения query-параметра format и их MIME-типы.
var formatMimeTypes = map[string]string{
	"jpeg": "image/jpeg",
	"png":  "image/png",
	"webp": "image/webp",
}

// AvatarService — операции с аватарками, которые нужны хендлеру.
type AvatarService interface {
	Upload(ctx context.Context, in services.UploadInput) (*domain.Avatar, error)
	Get(ctx context.Context, id uuid.UUID) (*domain.Avatar, error)
	ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error)
	Delete(ctx context.Context, userID string, id uuid.UUID) error
	DeleteCurrent(ctx context.Context, actorID, userID string) error
	ResolveFile(ctx context.Context, id uuid.UUID, opts services.FileOptions) (*services.File, error)
	ResolveCurrentFile(ctx context.Context, userID string, opts services.FileOptions) (*services.File, error)
	Open(ctx context.Context, f *services.File) (*domain.Object, error)
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
		URL:       avatarURL(a.ID),
		Status:    a.APIStatus(),
		CreatedAt: apiTime(a.CreatedAt),
	}
}

// metadataResponse — модель AvatarMetadata из API: метаданные и элемент списка.
type metadataResponse struct {
	ID         string              `json:"id"`
	UserID     string              `json:"user_id"`
	FileName   string              `json:"file_name"`
	MimeType   string              `json:"mime_type"`
	Size       int64               `json:"size"`
	Dimensions dimensionsResponse  `json:"dimensions"`
	Thumbnails []thumbnailResponse `json:"thumbnails"`
	Status     string              `json:"status"`
	CreatedAt  time.Time           `json:"created_at"`
	UpdatedAt  time.Time           `json:"updated_at"`
}

type dimensionsResponse struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type thumbnailResponse struct {
	Size string `json:"size"`
	URL  string `json:"url"`
}

func newMetadataResponse(a *domain.Avatar) metadataResponse {
	// В thumbnails только готовые миниатюры. Пустой слайс, а не nil: в JSON нужен [], а не null.
	thumbnails := make([]thumbnailResponse, 0, len(domain.ThumbnailSizes))
	for _, size := range domain.ThumbnailSizes {
		if _, ok := a.ThumbnailS3Keys[size]; ok {
			thumbnails = append(thumbnails, thumbnailResponse{
				Size: size,
				URL:  avatarURL(a.ID) + "?" + queryParamSize + "=" + size,
			})
		}
	}

	return metadataResponse{
		ID:         a.ID.String(),
		UserID:     a.UserID,
		FileName:   a.FileName,
		MimeType:   a.MimeType,
		Size:       a.SizeBytes,
		Dimensions: dimensionsResponse{Width: a.Width, Height: a.Height},
		Thumbnails: thumbnails,
		Status:     a.APIStatus(),
		CreatedAt:  apiTime(a.CreatedAt),
		UpdatedAt:  apiTime(a.UpdatedAt),
	}
}

// listResponse — ответ со списком: {"items": [...]}.
type listResponse struct {
	Items []metadataResponse `json:"items"`
}

// avatarURL — путь, по которому отдаётся картинка аватарки.
func avatarURL(id uuid.UUID) string {
	return "/api/v1/avatars/" + id.String()
}

// apiTime приводит время к формату API: RFC 3339 в UTC с точностью до секунды.
func apiTime(t time.Time) time.Time {
	return t.UTC().Truncate(time.Second)
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

// Get — GET /api/v1/avatars/{avatar_id}?size=&format=: отдаёт картинку потоком,
// не читая файл целиком в память.
func (h *AvatarHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseAvatarID(w, r)
	if !ok {
		return
	}
	opts, ok := parseFileOptions(w, r)
	if !ok {
		return
	}

	f, err := h.svc.ResolveFile(r.Context(), id, opts)
	h.writeFile(w, r, f, err)
}

// Metadata — GET /api/v1/avatars/{avatar_id}/metadata: описание аватарки
// и ссылки на готовые миниатюры.
func (h *AvatarHandler) Metadata(w http.ResponseWriter, r *http.Request) {
	id, ok := parseAvatarID(w, r)
	if !ok {
		return
	}

	a, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeServiceError(w, r, h.logger, err, h.maxUploadSize)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, newMetadataResponse(a))
}

// List — GET /api/v1/users/{user_id}/avatars: все неудалённые аватарки
// пользователя, новые сверху. Нет аватарок — {"items": []}.
func (h *AvatarHandler) List(w http.ResponseWriter, r *http.Request) {
	avatars, err := h.svc.ListByUser(r.Context(), chi.URLParam(r, urlParamUserID))
	if err != nil {
		writeServiceError(w, r, h.logger, err, h.maxUploadSize)
		return
	}

	resp := listResponse{Items: make([]metadataResponse, 0, len(avatars))}
	for i := range avatars {
		resp.Items = append(resp.Items, newMetadataResponse(&avatars[i]))
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// Delete — DELETE /api/v1/avatars/{avatar_id}: мягко удаляет свою аватарку
// и отвечает 204. Файлы из S3 удалит worker.
func (h *AvatarHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	id, ok := parseAvatarID(w, r)
	if !ok {
		return
	}

	if err := h.svc.Delete(r.Context(), userID, id); err != nil {
		writeServiceError(w, r, h.logger, err, h.maxUploadSize)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteCurrent — DELETE /api/v1/users/{user_id}/avatar: удаляет текущую
// аватарку пользователя. X-User-ID должен совпадать с user_id, иначе 403.
func (h *AvatarHandler) DeleteCurrent(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	if err := h.svc.DeleteCurrent(r.Context(), actorID, chi.URLParam(r, urlParamUserID)); err != nil {
		writeServiceError(w, r, h.logger, err, h.maxUploadSize)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetCurrent — GET /api/v1/users/{user_id}/avatar?size=&format=: как Get,
// но для текущей аватарки пользователя.
func (h *AvatarHandler) GetCurrent(w http.ResponseWriter, r *http.Request) {
	opts, ok := parseFileOptions(w, r)
	if !ok {
		return
	}

	f, err := h.svc.ResolveCurrentFile(r.Context(), chi.URLParam(r, urlParamUserID), opts)
	h.writeFile(w, r, f, err)
}

// writeFile отдаёт картинку, 304 на совпавший ETag или ошибку сервиса.
// Заголовки кеширования ставятся только на успешный ответ, чтобы ошибка не осела в кеше.
func (h *AvatarHandler) writeFile(w http.ResponseWriter, r *http.Request, f *services.File, err error) {
	if err != nil {
		writeServiceError(w, r, h.logger, err, h.maxUploadSize)
		return
	}

	if etagMatches(r.Header.Values("If-None-Match"), f.ETag) {
		setCacheHeaders(w, f)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	obj, err := h.svc.Open(r.Context(), f)
	if err != nil {
		writeServiceError(w, r, h.logger, err, h.maxUploadSize)
		return
	}
	defer obj.Body.Close()

	setCacheHeaders(w, f)
	w.Header().Set("Content-Type", obj.ContentType)
	// При конвертации формата размер заранее неизвестен — ответ уйдёт chunked.
	if obj.Size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(obj.Size, 10))
	}
	w.WriteHeader(http.StatusOK)
	// Заголовки уже ушли, поменять статус нельзя — остаётся только записать в лог.
	if _, err := io.Copy(w, obj.Body); err != nil {
		h.logger.WarnContext(r.Context(), "stream avatar", "path", r.URL.Path, "err", err)
	}
}

func setCacheHeaders(w http.ResponseWriter, f *services.File) {
	w.Header().Set("ETag", f.ETag)
	if f.Immutable {
		w.Header().Set("Cache-Control", cacheImmutable)
	} else {
		w.Header().Set("Cache-Control", cacheRevalidate)
	}
}

// etagMatches сравнивает If-None-Match с etag слабым сравнением (RFC 9110, 13.1.2):
// заголовок может быть списком тегов или *, префикс W/ не учитывается.
func etagMatches(ifNoneMatch []string, etag string) bool {
	want := strings.TrimPrefix(etag, "W/")
	for _, header := range ifNoneMatch {
		for _, tag := range strings.Split(header, ",") {
			tag = strings.TrimSpace(tag)
			if tag == "*" || strings.TrimPrefix(tag, "W/") == want {
				return true
			}
		}
	}
	return false
}

// parseFileOptions разбирает query-параметры size и format. Параметр, который
// передан с недопустимым значением (в том числе пустым), — 400 и false.
func parseFileOptions(w http.ResponseWriter, r *http.Request) (services.FileOptions, bool) {
	q := r.URL.Query()
	opts := services.FileOptions{Size: domain.SizeOriginal}

	if q.Has(queryParamSize) {
		opts.Size = q.Get(queryParamSize)
		if !allowedSizes[opts.Size] {
			httpx.WriteError(w, http.StatusBadRequest, msgInvalidRequest, detailsInvalidSize)
			return services.FileOptions{}, false
		}
	}
	if q.Has(queryParamFormat) {
		mimeType, ok := formatMimeTypes[q.Get(queryParamFormat)]
		if !ok {
			httpx.WriteError(w, http.StatusBadRequest, msgInvalidRequest, detailsInvalidFormatParam)
			return services.FileOptions{}, false
		}
		opts.Format = mimeType
	}
	return opts, true
}

// parseAvatarID разбирает {avatar_id} из пути. Если это не UUID, пишет 400 и возвращает false.
func parseAvatarID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, urlParamAvatarID))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, msgInvalidRequest, detailsInvalidAvatarID)
		return uuid.Nil, false
	}
	return id, true
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
