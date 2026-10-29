// Package worker — обработка событий брокера: создание миниатюр после
// загрузки и удаление файлов из S3 после удаления аватарки.
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // декодер для image.Decode
	_ "image/png"  // декодер для image.Decode
	"io"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	_ "golang.org/x/image/webp" // декодер для image.Decode

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/imaging"
	"go-avatar-service/internal/observability"
)

// tracerName — имя инструментирования в спанах worker'а.
const tracerName = "go-avatar-service/internal/worker"

// spanProcessThumbnails — бизнес-спан создания миниатюр одной аватарки.
const spanProcessThumbnails = "process_thumbnails"

// thumbnailSides — сторона квадрата миниатюры в пикселях по её размеру.
var thumbnailSides = map[string]int{
	domain.ThumbnailSize100: 100,
	domain.ThumbnailSize300: 300,
}

// Постоянные ошибки: повтор их не исправит, сообщение сразу уходит в DLQ.
var (
	ErrBadMessage   = errors.New("bad message")
	ErrBrokenImage  = errors.New("broken image")
	ErrUnknownEvent = errors.New("unknown event type")
)

// Repository — метаданные аватарок, которые нужны worker'у.
type Repository interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Avatar, error)
	SetProcessingStatus(ctx context.Context, id uuid.UUID, status domain.ProcessingStatus) error
	CompleteProcessing(ctx context.Context, id uuid.UUID, thumbnails map[string]string) error
}

// Storage — файлы в S3, которые нужны worker'у.
type Storage interface {
	Upload(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Download(ctx context.Context, key string) (*domain.Object, error)
	Delete(ctx context.Context, key string) error
}

// Processor обрабатывает события. Ошибки инфраструктуры (БД, S3) он помечает
// broker.Retryable, всё остальное — постоянные ошибки.
type Processor struct {
	repo    Repository
	storage Storage
	logger  *slog.Logger
}

// NewProcessor создаёт Processor.
func NewProcessor(repo Repository, storage Storage, logger *slog.Logger) *Processor {
	return &Processor{repo: repo, storage: storage, logger: logger}
}

// Handle — broker.Handler: выбирает обработку по типу события.
func (p *Processor) Handle(ctx context.Context, msg broker.Message) error {
	switch msg.Type {
	case broker.TypeAvatarUploaded:
		return p.handleUploaded(ctx, msg)
	case broker.TypeAvatarProcess:
		return p.handleProcess(ctx, msg)
	case broker.TypeAvatarDeleted:
		return p.handleDeleted(ctx, msg)
	default:
		return fmt.Errorf("%w: %q", ErrUnknownEvent, msg.Type)
	}
}

// handleUploaded создаёт все миниатюры. Удалённую аватарку и аватарку
// с готовыми миниатюрами пропускает: событие могли доставить повторно.
func (p *Processor) handleUploaded(ctx context.Context, msg broker.Message) error {
	var ev broker.AvatarUploadEvent
	if err := json.Unmarshal(msg.Body, &ev); err != nil {
		return fmt.Errorf("%w: decode %s: %w", ErrBadMessage, msg.Type, err)
	}
	id, err := uuid.Parse(ev.AvatarID)
	if err != nil {
		return fmt.Errorf("%w: avatar_id %q: %w", ErrBadMessage, ev.AvatarID, err)
	}

	a, err := p.repo.GetByID(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		p.logger.InfoContext(ctx, "avatar is deleted, skip processing", "avatar_id", id, "message_id", msg.ID)
		return nil
	}
	if err != nil {
		return broker.Retryable(err)
	}
	if a.ProcessingStatus == domain.ProcessingStatusCompleted {
		p.logger.InfoContext(ctx, "thumbnails already exist, skip processing", "avatar_id", id, "message_id", msg.ID)
		return nil
	}

	return p.makeThumbnails(ctx, a, domain.ThumbnailSizes)
}

// handleProcess пересоздаёт миниатюры из списка операций, даже если они уже
// готовы: ради этого событие и публикуют. Пустой список — все миниатюры.
func (p *Processor) handleProcess(ctx context.Context, msg broker.Message) error {
	var ev broker.AvatarProcessEvent
	if err := json.Unmarshal(msg.Body, &ev); err != nil {
		return fmt.Errorf("%w: decode %s: %w", ErrBadMessage, msg.Type, err)
	}
	id, err := uuid.Parse(ev.AvatarID)
	if err != nil {
		return fmt.Errorf("%w: avatar_id %q: %w", ErrBadMessage, ev.AvatarID, err)
	}

	sizes := domain.ThumbnailSizes
	if len(ev.Operations) > 0 {
		sizes = make([]string, 0, len(ev.Operations))
		for _, op := range ev.Operations {
			if _, ok := thumbnailSides[op.Size]; op.Type != broker.OpThumbnail || !ok {
				return fmt.Errorf("%w: unsupported operation %+v", ErrBadMessage, op)
			}
			sizes = append(sizes, op.Size)
		}
	}

	a, err := p.repo.GetByID(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		p.logger.InfoContext(ctx, "avatar is deleted, skip processing", "avatar_id", id, "message_id", msg.ID)
		return nil
	}
	if err != nil {
		return broker.Retryable(err)
	}
	return p.makeThumbnails(ctx, a, sizes)
}

// handleDeleted удаляет из S3 файлы из события. Отсутствующий файл — не
// ошибка, поэтому повторная доставка ничего не ломает.
func (p *Processor) handleDeleted(ctx context.Context, msg broker.Message) error {
	var ev broker.AvatarDeleteEvent
	if err := json.Unmarshal(msg.Body, &ev); err != nil {
		return fmt.Errorf("%w: decode %s: %w", ErrBadMessage, msg.Type, err)
	}

	for _, key := range ev.S3Keys {
		if err := p.storage.Delete(ctx, key); err != nil {
			return broker.Retryable(err)
		}
	}
	p.logger.InfoContext(ctx, "avatar files deleted", "avatar_id", ev.AvatarID, "keys", len(ev.S3Keys))
	return nil
}

// makeThumbnails скачивает оригинал, создаёт миниатюры sizes в JPEG, кладёт
// их в S3 и отмечает обработку законченной. Ключи миниатюр детерминированы,
// поэтому повтор после сбоя просто перезапишет те же файлы.
func (p *Processor) makeThumbnails(ctx context.Context, a *domain.Avatar, sizes []string) (err error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, spanProcessThumbnails, trace.WithAttributes(
		observability.AttrAvatarID.String(a.ID.String()),
		observability.AttrUserID.String(a.UserID),
		observability.AttrThumbnailSizes.StringSlice(sizes),
	))
	defer func() { observability.EndSpan(span, err, nil) }()

	if err := p.repo.SetProcessingStatus(ctx, a.ID, domain.ProcessingStatusProcessing); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil // удалили, пока событие шло
		}
		return broker.Retryable(err)
	}

	img, err := p.downloadImage(ctx, a.S3Key)
	if err != nil {
		return err
	}
	span.AddEvent("downloaded")

	keys := make(map[string]string, len(sizes))
	for _, size := range sizes {
		var buf bytes.Buffer
		if err := imaging.Encode(&buf, imaging.Thumbnail(img, thumbnailSides[size]), domain.ThumbnailMimeType); err != nil {
			return fmt.Errorf("encode thumbnail %s: %w", size, err)
		}
		key := domain.ThumbnailKey(a.ID, size)
		if err := p.storage.Upload(ctx, key, &buf, int64(buf.Len()), domain.ThumbnailMimeType); err != nil {
			return broker.Retryable(err)
		}
		keys[size] = key
	}
	span.AddEvent("stored")

	err = p.repo.CompleteProcessing(ctx, a.ID, keys)
	if errors.Is(err, domain.ErrNotFound) {
		// Аватарку удалили во время обработки, и worker мог уже удалить её файлы:
		// миниатюры, загруженные после этого, никто больше не уберёт.
		p.logger.InfoContext(ctx, "avatar deleted during processing, remove thumbnails", "avatar_id", a.ID)
		for _, key := range keys {
			if err := p.storage.Delete(ctx, key); err != nil {
				return broker.Retryable(err)
			}
		}
		return nil
	}
	if err != nil {
		return broker.Retryable(err)
	}

	p.logger.InfoContext(ctx, "thumbnails created", "avatar_id", a.ID, "sizes", sizes)
	return nil
}

// downloadImage скачивает и декодирует картинку. Нет файла или он не
// декодируется — ErrBrokenImage: повтор не поможет.
func (p *Processor) downloadImage(ctx context.Context, key string) (image.Image, error) {
	obj, err := p.storage.Download(ctx, key)
	if errors.Is(err, domain.ErrObjectNotFound) {
		return nil, fmt.Errorf("%w: original %s: %w", ErrBrokenImage, key, err)
	}
	if err != nil {
		return nil, broker.Retryable(err)
	}
	defer obj.Body.Close()

	img, _, err := image.Decode(obj.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: decode %s: %w", ErrBrokenImage, key, err)
	}
	return img, nil
}

// MarkFailed — обёртка над обработчиком: если создание миниатюр окончательно
// не удалось (постоянная ошибка или кончились повторы), ставит
// processing_status = failed. Ошибку возвращает дальше, чтобы сообщение ушло в DLQ.
func (p *Processor) MarkFailed(next broker.Handler) broker.Handler {
	return func(ctx context.Context, msg broker.Message) error {
		err := next(ctx, msg)
		if err == nil || ctx.Err() != nil ||
			(msg.Type != broker.TypeAvatarUploaded && msg.Type != broker.TypeAvatarProcess) {
			return err
		}

		id, parseErr := uuid.Parse(msg.Key)
		if parseErr != nil {
			return err
		}
		// Контекст может быть на исходе: статус всё равно нужно записать.
		if setErr := p.repo.SetProcessingStatus(context.WithoutCancel(ctx), id, domain.ProcessingStatusFailed); setErr != nil &&
			!errors.Is(setErr, domain.ErrNotFound) {
			p.logger.ErrorContext(ctx, "mark processing failed", "avatar_id", id, "err", setErr)
		}
		return err
	}
}
