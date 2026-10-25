package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // декодер для image.DecodeConfig и image.Decode
	_ "image/png"  // декодер для image.DecodeConfig и image.Decode
	"io"
	"net/http"

	"github.com/google/uuid"
	_ "golang.org/x/image/webp" // декодер для image.DecodeConfig и image.Decode

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/imaging"
)

// sniffLen — сколько первых байт файла смотрит http.DetectContentType.
const sniffLen = 512

// maxFileNameLength — длина file_name в таблице avatars.
const maxFileNameLength = 255

// supportedFormats — поддерживаемые MIME-типы и расширения оригинала в S3.
var supportedFormats = map[string]string{
	"image/jpeg": "jpg",
	"image/png":  "png",
	"image/webp": "webp",
}

// AvatarRepository — хранилище метаданных аватарок, которое нужно сервису.
type AvatarRepository interface {
	Create(ctx context.Context, a *domain.Avatar) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Avatar, error)
	GetCurrentByUser(ctx context.Context, userID string) (*domain.Avatar, error)
	ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error)
	SoftDelete(ctx context.Context, id uuid.UUID) error
}

// Storage — файловое хранилище (S3), которое нужно сервису.
type Storage interface {
	// Upload кладёт файл по ключу. size — размер в байтах, -1 если неизвестен.
	Upload(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Download отдаёт файл потоком или domain.ErrObjectNotFound. Body закрывает вызывающий.
	Download(ctx context.Context, key string) (*domain.Object, error)
	// Delete удаляет файл. Отсутствующий файл — не ошибка.
	Delete(ctx context.Context, key string) error
	BucketExists(ctx context.Context) (bool, error)
}

// Publisher — публикация событий в брокер, которая нужна сервису.
type Publisher interface {
	Publish(ctx context.Context, msg broker.Message) error
}

// AvatarService — бизнес-логика аватарок.
type AvatarService struct {
	repo          AvatarRepository
	storage       Storage
	publisher     Publisher
	maxUploadSize int64
}

// NewAvatarService создаёт сервис. maxUploadSize — лимит размера файла в байтах.
func NewAvatarService(repo AvatarRepository, storage Storage, publisher Publisher, maxUploadSize int64) *AvatarService {
	return &AvatarService{repo: repo, storage: storage, publisher: publisher, maxUploadSize: maxUploadSize}
}

// UploadInput — загружаемый файл. File читается с начала несколько раз,
// поэтому нужен io.ReadSeeker (multipart.File им является).
type UploadInput struct {
	UserID   string
	FileName string
	File     io.ReadSeeker
	Size     int64
}

// Upload сохраняет оригинал в S3, строку в БД со статусом pending и публикует
// avatar.uploaded. Миниатюры не ждёт: их создаст worker.
// Если публикация не удалась, строка остаётся pending и возвращается ошибка.
func (s *AvatarService) Upload(ctx context.Context, in UploadInput) (*domain.Avatar, error) {
	if in.Size > s.maxUploadSize {
		return nil, domain.ErrTooLarge
	}

	mimeType, ext, err := detectFormat(in.File)
	if err != nil {
		return nil, err
	}
	width, height, err := decodeDimensions(in.File)
	if err != nil {
		return nil, err
	}
	if _, err := in.File.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind file: %w", err)
	}

	id := uuid.New()
	key := domain.OriginalKey(id, ext)
	if err := s.storage.Upload(ctx, key, in.File, in.Size, mimeType); err != nil {
		return nil, err
	}

	a := &domain.Avatar{
		ID:               id,
		UserID:           in.UserID,
		FileName:         truncateRunes(in.FileName, maxFileNameLength),
		MimeType:         mimeType,
		SizeBytes:        in.Size,
		Width:            width,
		Height:           height,
		S3Key:            key,
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusPending,
	}
	if err := s.repo.Create(ctx, a); err != nil {
		// Без строки в БД оригинал никто не найдёт — убираем его. Контекст запроса
		// может быть уже отменён, а удалить файл всё равно нужно.
		if delErr := s.storage.Delete(context.WithoutCancel(ctx), key); delErr != nil {
			return nil, errors.Join(err, fmt.Errorf("cleanup original: %w", delErr))
		}
		return nil, err
	}

	if err := s.publishUploaded(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// FileOptions — какую картинку отдать. Значения уже проверены хендлером.
type FileOptions struct {
	// Size — domain.ThumbnailSize100, domain.ThumbnailSize300 или domain.SizeOriginal.
	Size string
	// Format — MIME-тип ответа. Пусто — формат файла как есть.
	Format string
}

// GetFile отдаёт картинку аватарки потоком. Удалённой или несуществующей
// аватарки нет — domain.ErrNotFound. Body закрывает вызывающий.
func (s *AvatarService) GetFile(ctx context.Context, id uuid.UUID, opts FileOptions) (*domain.Object, error) {
	a, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.openFile(ctx, a, opts)
}

// GetCurrentFile — как GetFile, но для текущей аватарки пользователя:
// последней загруженной неудалённой. Аватарок нет — domain.ErrNotFound.
func (s *AvatarService) GetCurrentFile(ctx context.Context, userID string, opts FileOptions) (*domain.Object, error) {
	a, err := s.repo.GetCurrentByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.openFile(ctx, a, opts)
}

// openFile выбирает ключ S3 по размеру и при необходимости конвертирует формат.
// ContentType берётся из БД (или из формата миниатюр), а не из S3.
func (s *AvatarService) openFile(ctx context.Context, a *domain.Avatar, opts FileOptions) (*domain.Object, error) {
	key, mimeType := a.S3Key, a.MimeType
	// Миниатюры ещё нет — отдаём оригинал: клиент всегда получает картинку, а не ошибку.
	if thumbKey, ok := a.ThumbnailS3Keys[opts.Size]; ok {
		key, mimeType = thumbKey, domain.ThumbnailMimeType
	}

	obj, err := s.storage.Download(ctx, key)
	if err != nil {
		return nil, err
	}
	if opts.Format == "" || opts.Format == mimeType {
		obj.ContentType = mimeType
		return obj, nil
	}
	return convert(obj, opts.Format)
}

// convert перекодирует картинку в mimeType. Декодировать приходится целиком,
// а кодирование идёт через pipe по мере того, как вызывающий читает Body,
// поэтому размер заранее неизвестен (Size = -1). Если вызывающий закроет Body
// раньше, запись в pipe вернёт ошибку и горутина завершится.
func convert(obj *domain.Object, mimeType string) (*domain.Object, error) {
	defer obj.Body.Close()

	img, _, err := image.Decode(obj.Body)
	if err != nil {
		return nil, fmt.Errorf("decode for conversion to %s: %w", mimeType, err)
	}

	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(imaging.Encode(pw, img, mimeType))
	}()
	return &domain.Object{Body: pr, Size: -1, ContentType: mimeType}, nil
}

func (s *AvatarService) publishUploaded(ctx context.Context, a *domain.Avatar) error {
	body, err := json.Marshal(broker.AvatarUploadEvent{
		AvatarID: a.ID.String(),
		UserID:   a.UserID,
		S3Key:    a.S3Key,
	})
	if err != nil {
		return fmt.Errorf("marshal %s event: %w", broker.TypeAvatarUploaded, err)
	}

	err = s.publisher.Publish(ctx, broker.Message{
		ID:   uuid.NewString(),
		Type: broker.TypeAvatarUploaded,
		Key:  a.ID.String(),
		Body: body,
	})
	if err != nil {
		return fmt.Errorf("publish %s: %w", broker.TypeAvatarUploaded, err)
	}
	return nil
}

// detectFormat определяет формат по первым байтам файла, а не по имени
// или Content-Type от клиента. Неподдерживаемый формат — domain.ErrInvalidFormat.
func detectFormat(r io.ReadSeeker) (mimeType, ext string, err error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", "", fmt.Errorf("rewind file: %w", err)
	}

	buf := make([]byte, sniffLen)
	n, err := io.ReadFull(r, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", "", fmt.Errorf("read file header: %w", err)
	}

	mimeType = http.DetectContentType(buf[:n])
	ext, ok := supportedFormats[mimeType]
	if !ok {
		return "", "", domain.ErrInvalidFormat
	}
	return mimeType, ext, nil
}

// decodeDimensions читает размер картинки из заголовка, не декодируя её целиком.
// Файл с правильной сигнатурой, но битым заголовком — domain.ErrInvalidFormat.
func decodeDimensions(r io.ReadSeeker) (width, height int, err error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, 0, fmt.Errorf("rewind file: %w", err)
	}

	cfg, _, err := image.DecodeConfig(r)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: decode image config: %v", domain.ErrInvalidFormat, err)
	}
	return cfg.Width, cfg.Height, nil
}

// truncateRunes обрезает s до n символов, не разрезая UTF-8.
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
