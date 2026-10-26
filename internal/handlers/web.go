package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/services"
)

// Шаблоны веб-интерфейса из пакета web.
const (
	templateUpload  = "upload.html"
	templateGallery = "gallery.html"
)

// formFieldUserID — поле формы /web/upload с user_id: в HTML-форме нельзя
// поставить заголовок X-User-ID.
const formFieldUserID = "user_id"

// WebService — операции с аватарками, которые нужны веб-интерфейсу.
type WebService interface {
	Upload(ctx context.Context, in services.UploadInput) (*domain.Avatar, error)
	ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error)
}

// WebHandler обслуживает веб-интерфейс /web: форму загрузки и галерею.
type WebHandler struct {
	svc           WebService
	templates     *template.Template
	maxUploadSize int64
	logger        *slog.Logger
}

// NewWebHandler создаёт WebHandler. templates должны содержать upload.html и gallery.html.
func NewWebHandler(svc WebService, templates *template.Template, maxUploadSize int64, logger *slog.Logger) *WebHandler {
	return &WebHandler{svc: svc, templates: templates, maxUploadSize: maxUploadSize, logger: logger}
}

// uploadPage — данные шаблона формы загрузки.
type uploadPage struct {
	UserID string
	Error  string
}

// galleryPage — данные шаблона галереи.
type galleryPage struct {
	UserID  string
	Avatars []galleryItem
}

// galleryItem — карточка аватарки в галерее.
type galleryItem struct {
	ID           string
	FileName     string
	Format       string
	Width        int
	Height       int
	Size         string
	Status       string
	URL          string
	ThumbnailURL string
}

// Root — GET /: редирект на форму загрузки.
func (h *WebHandler) Root(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/web/upload", http.StatusFound)
}

// UploadForm — GET /web/upload: форма загрузки. ?user_id= подставляется в поле.
func (h *WebHandler) UploadForm(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, templateUpload, uploadPage{UserID: r.URL.Query().Get(formFieldUserID)})
}

// Upload — POST /web/upload: приём HTML-формы без JavaScript. Логика та же,
// что у POST /api/v1/avatars, после успеха — 303 на галерею пользователя,
// при ошибке — снова форма с текстом ошибки.
func (h *WebHandler) Upload(w http.ResponseWriter, r *http.Request) {
	if err := parseMultipart(w, r, h.maxUploadSize); err != nil {
		if errors.Is(err, domain.ErrTooLarge) {
			h.renderUploadError(w, r, "", http.StatusRequestEntityTooLarge, h.tooLargeMessage())
			return
		}
		h.renderUploadError(w, r, "", http.StatusBadRequest, detailsNotMultipart)
		return
	}
	defer removeMultipart(r, h.logger)

	userID := r.FormValue(formFieldUserID)
	if details := validateUserID(userID); details != "" {
		h.renderUploadError(w, r, userID, http.StatusBadRequest, details)
		return
	}

	file, header, err := r.FormFile(formFieldFile)
	if err != nil {
		h.renderUploadError(w, r, userID, http.StatusBadRequest, detailsFileRequired)
		return
	}
	defer file.Close()

	_, err = h.svc.Upload(r.Context(), services.UploadInput{
		UserID:   userID,
		FileName: header.Filename,
		File:     file,
		Size:     header.Size,
	})
	if err != nil {
		status, resp := serviceErrorResponse(err, h.maxUploadSize)
		msg := resp.Error
		switch {
		case status == http.StatusRequestEntityTooLarge:
			msg = h.tooLargeMessage()
		case status == http.StatusInternalServerError:
			h.logger.ErrorContext(r.Context(), "unhandled service error", "method", r.Method, "path", r.URL.Path, "err", err)
		case resp.Details != "":
			msg += ": " + resp.Details
		}
		h.renderUploadError(w, r, userID, status, msg)
		return
	}

	http.Redirect(w, r, galleryPath(userID), http.StatusSeeOther)
}

// Gallery — GET /web/gallery/{user_id}: миниатюры 300x300 всех аватарок
// пользователя с размером, форматом, статусом и кнопкой удаления.
func (h *WebHandler) Gallery(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, urlParamUserID)

	avatars, err := h.svc.ListByUser(r.Context(), userID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "list avatars for gallery", "user_id", userID, "err", err)
		http.Error(w, msgInternal, http.StatusInternalServerError)
		return
	}

	page := galleryPage{UserID: userID, Avatars: make([]galleryItem, 0, len(avatars))}
	for i := range avatars {
		a := &avatars[i]
		page.Avatars = append(page.Avatars, galleryItem{
			ID:       a.ID.String(),
			FileName: a.FileName,
			Format:   strings.ToUpper(strings.TrimPrefix(a.MimeType, "image/")),
			Width:    a.Width,
			Height:   a.Height,
			Size:     humanSize(a.SizeBytes),
			Status:   a.APIStatus(),
			URL:      avatarURL(a.ID),
			// Пока миниатюры нет, сервис отдаёт по этому URL оригинал.
			ThumbnailURL: avatarURL(a.ID) + "?" + queryParamSize + "=" + domain.ThumbnailSize300,
		})
	}
	h.render(w, r, http.StatusOK, templateGallery, page)
}

func (h *WebHandler) renderUploadError(w http.ResponseWriter, r *http.Request, userID string, status int, msg string) {
	h.render(w, r, status, templateUpload, uploadPage{UserID: userID, Error: msg})
}

func (h *WebHandler) tooLargeMessage() string {
	return fmt.Sprintf("%s: max %s", msgTooLarge, humanSize(h.maxUploadSize))
}

// render выполняет шаблон в буфер, чтобы ошибка шаблона не оставила
// клиенту половину страницы со статусом 200.
func (h *WebHandler) render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	var buf bytes.Buffer
	if err := h.templates.ExecuteTemplate(&buf, name, data); err != nil {
		h.logger.ErrorContext(r.Context(), "render template", "template", name, "err", err)
		http.Error(w, msgInternal, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		h.logger.WarnContext(r.Context(), "write page", "template", name, "err", err)
	}
}

// galleryPath — путь галереи пользователя. user_id — произвольная строка,
// поэтому экранируется.
func galleryPath(userID string) string {
	return "/web/gallery/" + url.PathEscape(userID)
}

// humanSize форматирует размер в байтах: 512 B, 1.5 KB, 10.0 MB.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
