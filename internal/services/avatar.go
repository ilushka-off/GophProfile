package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // декодер для image.DecodeConfig и image.Decode
	_ "image/png"  // декодер для image.DecodeConfig и image.Decode
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	_ "golang.org/x/image/webp" // декодер для image.DecodeConfig и image.Decode

	"go-avatar-service/internal/broker"
	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/imaging"
	"go-avatar-service/internal/observability"
)

// sniffLen — сколько первых байт файла смотрит http.DetectContentType.
const sniffLen = 512

// maxFileNameLength — длина file_name в таблице avatars.
const maxFileNameLength = 255

// imageFormat — поддерживаемый формат оригинала.
type imageFormat struct {
	// ext — расширение оригинала в S3.
	ext string
	// decoder — имя формата, которое возвращает image.DecodeConfig.
	decoder string
	// magic проверяет сигнатуру файла.
	magic func(header []byte) bool
}

// supportedFormats — поддерживаемые форматы по MIME-типу.
var supportedFormats = map[string]imageFormat{
	"image/jpeg": {ext: "jpg", decoder: "jpeg", magic: func(h []byte) bool {
		return bytes.HasPrefix(h, []byte{0xFF, 0xD8, 0xFF})
	}},
	"image/png": {ext: "png", decoder: "png", magic: func(h []byte) bool {
		return bytes.HasPrefix(h, []byte("\x89PNG\r\n\x1a\n"))
	}},
	"image/webp": {ext: "webp", decoder: "webp", magic: func(h []byte) bool {
		return len(h) >= 12 && bytes.Equal(h[0:4], []byte("RIFF")) && bytes.Equal(h[8:12], []byte("WEBP"))
	}},
}

// maxPixels — предел ширина × высота. Маленький по размеру файл может
// разворачиваться в огромную картинку: worker декодирует её целиком в память.
const maxPixels = 50_000_000

// mimeOctetStream — Content-Type, который клиенты ставят, когда не знают тип файла.
const mimeOctetStream = "application/octet-stream"

// Имена бизнес-спанов: сценарий целиком, внутри — спаны БД, S3 и брокера.
const (
	spanUploadAvatar = "upload_avatar"
	spanDeleteAvatar = "delete_avatar"
)

// tracer берётся из глобального provider при каждом вызове, а не один раз
// при инициализации пакета: так спаны идут в provider, настроенный в main.
func tracer() trace.Tracer {
	return otel.Tracer("go-avatar-service/internal/services")
}

// IsClientError — ошибка вызвана запросом клиента (формат, размер, права,
// нет аватарки), а не сбоем сервиса. Такие ошибки не помечают спан ошибкой.
func IsClientError(err error) bool {
	var validation *domain.ValidationError
	return errors.Is(err, domain.ErrInvalidFormat) || errors.Is(err, domain.ErrTooLarge) ||
		errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrForbidden) ||
		errors.As(err, &validation)
}

// AvatarRepository — хранилище метаданных аватарок, которое нужно сервису.
type AvatarRepository interface {
	Create(ctx context.Context, a *domain.Avatar) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Avatar, error)
	GetCurrentByUser(ctx context.Context, userID string) (*domain.Avatar, error)
	ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error)
	SoftDelete(ctx context.Context, id uuid.UUID) error
	Restore(ctx context.Context, id uuid.UUID) error
	Delete(ctx context.Context, id uuid.UUID) error
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
	metrics       *Metrics
}

// MaxUploadSizeLimit — верхняя граница лимита на размер файла: сервис
// не принимает файлы крупнее 10 MiB, как бы ни был настроен лимит.
const MaxUploadSizeLimit = 10 << 20

// NewAvatarService создаёт сервис. maxUploadSize — лимит размера файла в байтах,
// от 1 до MaxUploadSizeLimit.
func NewAvatarService(repo AvatarRepository, storage Storage, publisher Publisher, maxUploadSize int64, opts ...Option) (*AvatarService, error) {
	if maxUploadSize < 1 || maxUploadSize > MaxUploadSizeLimit {
		return nil, fmt.Errorf("max upload size must be between 1 and %d bytes, got %d", MaxUploadSizeLimit, maxUploadSize)
	}
	s := &AvatarService{repo: repo, storage: storage, publisher: publisher, maxUploadSize: maxUploadSize}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// UploadInput — загружаемый файл. File читается с начала несколько раз,
// поэтому нужен io.ReadSeeker (multipart.File им является).
type UploadInput struct {
	UserID   string
	FileName string
	File     io.ReadSeeker
	Size     int64
	// ContentType — тип файла, заявленный клиентом в части multipart. Ему не
	// доверяют, но если он задан и не совпадает с содержимым — это отказ.
	ContentType string
}

// Upload сохраняет оригинал в S3, строку в БД со статусом pending и публикует
// avatar.uploaded. Миниатюры не ждёт: их создаст worker.
// Если публикация не удалась, строка и оригинал удаляются: ошибка значит,
// что ничего не сохранилось, и запрос можно повторить.
func (s *AvatarService) Upload(ctx context.Context, in UploadInput) (a *domain.Avatar, err error) {
	ctx, span := tracer().Start(ctx, spanUploadAvatar, trace.WithAttributes(
		observability.AttrUserID.String(in.UserID),
		observability.AttrFileSize.Int64(in.Size),
	))
	start := time.Now()
	defer func() {
		s.metrics.observeUpload(start, err)
		observability.EndSpan(span, err, IsClientError)
	}()

	if in.Size > s.maxUploadSize {
		return nil, domain.ErrTooLarge
	}

	mimeType, format, err := detectFormat(in.File)
	if err != nil {
		return nil, err
	}
	span.SetAttributes(observability.AttrMimeType.String(mimeType))
	if err := checkDeclaredType(in.ContentType, mimeType); err != nil {
		return nil, err
	}
	width, height, err := decodeDimensions(in.File, format)
	if err != nil {
		return nil, err
	}
	span.AddEvent("validated")
	ext := format.ext
	if _, err := in.File.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind file: %w", err)
	}

	id := uuid.New()
	span.SetAttributes(observability.AttrAvatarID.String(id.String()))
	key := domain.OriginalKey(id, ext)
	if err := s.storage.Upload(ctx, key, in.File, in.Size, mimeType); err != nil {
		return nil, err
	}

	a = &domain.Avatar{
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
	span.AddEvent("stored")

	if err := s.publishUploaded(ctx, a); err != nil {
		// Без события миниатюры никто не создаст, и аватарка навсегда осталась бы
		// в processing. Если брокер всё же принял сообщение, worker не найдёт
		// строку и пропустит его.
		return nil, s.rollbackUpload(ctx, a, err)
	}
	span.AddEvent("published")
	return a, nil
}

// rollbackUpload удаляет строку и оригинал после неудачной публикации и
// возвращает err вместе с ошибками отката. Контекст запроса может быть уже
// отменён, а откатить всё равно нужно.
func (s *AvatarService) rollbackUpload(ctx context.Context, a *domain.Avatar, err error) error {
	ctx = context.WithoutCancel(ctx)
	if delErr := s.repo.Delete(ctx, a.ID); delErr != nil {
		// Оригинал оставляем: на него ссылается строка.
		return errors.Join(err, fmt.Errorf("rollback avatar row: %w", delErr))
	}
	if delErr := s.storage.Delete(ctx, a.S3Key); delErr != nil {
		return errors.Join(err, fmt.Errorf("cleanup original: %w", delErr))
	}
	return err
}

// Get возвращает неудалённую аватарку по id или domain.ErrNotFound.
func (s *AvatarService) Get(ctx context.Context, id uuid.UUID) (*domain.Avatar, error) {
	return s.repo.GetByID(ctx, id)
}

// ListByUser возвращает неудалённые аватарки пользователя, новые сверху.
// Пользователь без аватарок — не ошибка, а пустой слайс.
func (s *AvatarService) ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error) {
	return s.repo.ListByUser(ctx, userID)
}

// FileOptions — какую картинку отдать. Значения уже проверены хендлером.
type FileOptions struct {
	// Size — domain.ThumbnailSize100, domain.ThumbnailSize300 или domain.SizeOriginal.
	Size string
	// Format — MIME-тип ответа. Пусто — формат файла как есть.
	Format string
}

// File — картинка, которую отдаст Open. Всё, кроме самих байт, известно
// до похода в S3, поэтому на совпавший ETag можно ответить 304, не скачивая
// и не конвертируя файл.
type File struct {
	// ContentType — формат ответа: из БД, формат миниатюр или запрошенный format.
	ContentType string
	ETag        string
	// Immutable — по этому URL всегда будет тот же файл, и его можно кешировать
	// надолго. Это не так для текущей аватарки пользователя и для оригинала,
	// отданного вместо неготовой миниатюры.
	Immutable bool

	key        string
	sourceType string
}

// ResolveFile находит, какую картинку отдать по avatar_id. Удалённой или
// несуществующей аватарки нет — domain.ErrNotFound.
func (s *AvatarService) ResolveFile(ctx context.Context, id uuid.UUID, opts FileOptions) (*File, error) {
	a, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return resolveFile(a, opts, true), nil
}

// ResolveCurrentFile — как ResolveFile, но для текущей аватарки пользователя:
// последней загруженной неудалённой. Аватарок нет — domain.ErrNotFound.
func (s *AvatarService) ResolveCurrentFile(ctx context.Context, userID string, opts FileOptions) (*File, error) {
	a, err := s.repo.GetCurrentByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	// Текущая аватарка меняется после новой загрузки или удаления.
	return resolveFile(a, opts, false), nil
}

// Open скачивает файл потоком и при необходимости конвертирует формат.
// Body закрывает вызывающий.
func (s *AvatarService) Open(ctx context.Context, f *File) (*domain.Object, error) {
	obj, err := s.storage.Download(ctx, f.key)
	if err != nil {
		return nil, err
	}
	if f.ContentType == f.sourceType {
		obj.ContentType = f.ContentType
		return obj, nil
	}
	return convert(obj, f.ContentType)
}

// resolveFile выбирает ключ S3 по размеру и формат ответа. ContentType берётся
// из БД (или из формата миниатюр), а не из S3. stableURL — URL указывает
// на конкретную аватарку, а не на «текущую».
func resolveFile(a *domain.Avatar, opts FileOptions, stableURL bool) *File {
	f := &File{key: a.S3Key, sourceType: a.MimeType, Immutable: stableURL}

	thumbKey, ready := a.ThumbnailS3Keys[opts.Size]
	switch {
	case ready:
		f.key, f.sourceType = thumbKey, domain.ThumbnailMimeType
	case opts.Size == domain.ThumbnailSize100 || opts.Size == domain.ThumbnailSize300:
		// Миниатюры ещё нет — отдаём оригинал: клиент всегда получает картинку,
		// а не ошибку. Но после обработки по этому URL будет уже миниатюра.
		f.Immutable = false
	}

	f.ContentType = f.sourceType
	if opts.Format != "" {
		f.ContentType = opts.Format
	}
	f.ETag = etag(f.key, f.ContentType, f.ContentType != f.sourceType)
	return f
}

// etag считается от ключа S3 и формата ответа: файл по ключу не меняется.
// У сконвертированной картинки ETag слабый: те же пиксели, но байты зависят
// от настроек кодировщика.
func etag(key, contentType string, converted bool) string {
	sum := sha256.Sum256([]byte(key + "|" + contentType))
	tag := `"` + hex.EncodeToString(sum[:16]) + `"`
	if converted {
		return "W/" + tag
	}
	return tag
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

// Delete мягко удаляет аватарку id от имени userID и публикует avatar.deleted:
// файлы из S3 удалит worker. Чужая аватарка — domain.ErrForbidden,
// удалённая или несуществующая — domain.ErrNotFound.
func (s *AvatarService) Delete(ctx context.Context, userID string, id uuid.UUID) (err error) {
	ctx, span := tracer().Start(ctx, spanDeleteAvatar, trace.WithAttributes(
		observability.AttrUserID.String(userID),
		observability.AttrAvatarID.String(id.String()),
	))
	defer func() {
		s.metrics.observeDelete(err)
		observability.EndSpan(span, err, IsClientError)
	}()

	a, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if a.UserID != userID {
		return domain.ErrForbidden
	}
	return s.softDelete(ctx, a)
}

// DeleteCurrent удаляет текущую аватарку пользователя userID от имени actorID,
// как Delete. Удалять можно только у себя: actorID ≠ userID — domain.ErrForbidden.
// Аватарок нет — domain.ErrNotFound.
func (s *AvatarService) DeleteCurrent(ctx context.Context, actorID, userID string) (err error) {
	ctx, span := tracer().Start(ctx, spanDeleteAvatar, trace.WithAttributes(observability.AttrUserID.String(actorID)))
	defer func() {
		s.metrics.observeDelete(err)
		observability.EndSpan(span, err, IsClientError)
	}()

	if actorID != userID {
		return domain.ErrForbidden
	}
	a, err := s.repo.GetCurrentByUser(ctx, userID)
	if err != nil {
		return err
	}
	span.SetAttributes(observability.AttrAvatarID.String(a.ID.String()))
	return s.softDelete(ctx, a)
}

// softDelete помечает аватарку удалённой и публикует avatar.deleted. Если
// публикация не удалась, пометка снимается: иначе повторный DELETE вернул бы
// 404, а файлы навсегда остались бы в S3. Ошибка значит, что аватарка на месте
// и запрос можно повторить.
func (s *AvatarService) softDelete(ctx context.Context, a *domain.Avatar) error {
	if err := s.repo.SoftDelete(ctx, a.ID); err != nil {
		return err
	}

	// Ключи миниатюр берутся по всем размерам, а не из thumbnail_s3_keys:
	// worker может дописать миниатюры уже после того, как событие ушло.
	keys := make([]string, 0, 1+len(domain.ThumbnailSizes))
	keys = append(keys, a.S3Key)
	for _, size := range domain.ThumbnailSizes {
		keys = append(keys, domain.ThumbnailKey(a.ID, size))
	}

	err := s.publish(ctx, broker.TypeAvatarDeleted, a.ID, broker.AvatarDeleteEvent{
		AvatarID: a.ID.String(),
		S3Keys:   keys,
	})
	if err != nil {
		// Если брокер всё же принял сообщение (например, не дождались
		// подтверждения), worker удалит файлы восстановленной аватарки.
		// Надёжно это решает только transactional outbox.
		if restoreErr := s.repo.Restore(context.WithoutCancel(ctx), a.ID); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("rollback soft delete: %w", restoreErr))
		}
		return err
	}
	return nil
}

func (s *AvatarService) publishUploaded(ctx context.Context, a *domain.Avatar) error {
	return s.publish(ctx, broker.TypeAvatarUploaded, a.ID, broker.AvatarUploadEvent{
		AvatarID: a.ID.String(),
		UserID:   a.UserID,
		S3Key:    a.S3Key,
	})
}

// publish отправляет событие eventType с новым message_id и ключом avatarID.
func (s *AvatarService) publish(ctx context.Context, eventType string, avatarID uuid.UUID, event any) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal %s event: %w", eventType, err)
	}

	err = s.publisher.Publish(ctx, broker.Message{
		ID:   uuid.NewString(),
		Type: eventType,
		Key:  avatarID.String(),
		Body: body,
	})
	if err != nil {
		return fmt.Errorf("publish %s: %w", eventType, err)
	}
	return nil
}

// detectFormat определяет формат по первым байтам файла, а не по имени
// или Content-Type от клиента, и дополнительно сверяет сигнатуру формата.
// Неподдерживаемый формат — domain.ErrInvalidFormat.
func detectFormat(r io.ReadSeeker) (string, imageFormat, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", imageFormat{}, fmt.Errorf("rewind file: %w", err)
	}

	buf := make([]byte, sniffLen)
	n, err := io.ReadFull(r, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", imageFormat{}, fmt.Errorf("read file header: %w", err)
	}

	mimeType := http.DetectContentType(buf[:n])
	format, ok := supportedFormats[mimeType]
	if !ok || !format.magic(buf[:n]) {
		return "", imageFormat{}, domain.ErrInvalidFormat
	}
	return mimeType, format, nil
}

// checkDeclaredType сверяет Content-Type, заявленный клиентом, с настоящим
// типом файла. Пустой и application/octet-stream ничего не утверждают.
func checkDeclaredType(declared, actual string) error {
	mediaType, _, err := mime.ParseMediaType(declared)
	if declared == "" || (err == nil && mediaType == mimeOctetStream) {
		return nil
	}
	if err != nil || !strings.EqualFold(mediaType, actual) {
		return &domain.ValidationError{Details: fmt.Sprintf("file content is %s, but Content-Type is %q", actual, declared)}
	}
	return nil
}

// decodeDimensions читает размер картинки из заголовка, не декодируя её целиком.
// Битый заголовок или заголовок другого формата — domain.ErrInvalidFormat,
// слишком большая картинка — ошибка валидации.
func decodeDimensions(r io.ReadSeeker, format imageFormat) (width, height int, err error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, 0, fmt.Errorf("rewind file: %w", err)
	}

	cfg, decoder, err := image.DecodeConfig(r)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: decode image config: %w", domain.ErrInvalidFormat, err)
	}
	if decoder != format.decoder {
		return 0, 0, fmt.Errorf("%w: decoded as %s, want %s", domain.ErrInvalidFormat, decoder, format.decoder)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return 0, 0, &domain.ValidationError{Details: fmt.Sprintf(
			"image is %dx%d, at most %d megapixels are allowed", cfg.Width, cfg.Height, maxPixels/1_000_000)}
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
