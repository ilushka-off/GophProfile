package broker

// Типы событий. В RabbitMQ это routing key, в Kafka — заголовок event_type.
const (
	TypeAvatarUploaded = "avatar.uploaded"
	TypeAvatarProcess  = "avatar.process"
	TypeAvatarDeleted  = "avatar.deleted"
)

// AvatarUploadEvent публикует server после загрузки: worker создаёт по нему миниатюры.
type AvatarUploadEvent struct {
	AvatarID string `json:"avatar_id"`
	UserID   string `json:"user_id"`
	S3Key    string `json:"s3_key"`
}

// AvatarDeleteEvent публикует server после мягкого удаления: worker удаляет
// по нему файлы из S3. В S3Keys — оригинал и все миниатюры.
type AvatarDeleteEvent struct {
	AvatarID string   `json:"avatar_id"`
	S3Keys   []string `json:"s3_keys"`
}
