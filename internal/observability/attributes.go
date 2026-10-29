package observability

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Атрибуты бизнес-спанов из ТЗ.
const (
	AttrUserID         = attribute.Key("user.id")
	AttrAvatarID       = attribute.Key("avatar.id")
	AttrFileSize       = attribute.Key("file.size")
	AttrMimeType       = attribute.Key("mime.type")
	AttrThumbnailSizes = attribute.Key("thumbnail.sizes")
)

// EndSpan завершает спан с результатом err. Ошибки клиента (isClientError
// вернул true: неверный формат, чужая аватарка) — ожидаемый исход, спан ими
// не помечается, вместо этого пишется событие rejected с причиной. Остальные
// ошибки записываются в спан, и он получает статус Error.
func EndSpan(span trace.Span, err error, isClientError func(error) bool) {
	defer span.End()
	if err == nil {
		return
	}
	if isClientError != nil && isClientError(err) {
		span.AddEvent("rejected", trace.WithAttributes(attribute.String("reason", err.Error())))
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
