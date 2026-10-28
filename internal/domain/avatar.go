package domain

import (
	"time"

	"github.com/google/uuid"
)

// UploadStatus — статус загрузки оригинала (upload_status).
type UploadStatus string

const (
	UploadStatusUploading UploadStatus = "uploading"
	UploadStatusUploaded  UploadStatus = "uploaded"
	UploadStatusFailed    UploadStatus = "failed"
)

// ProcessingStatus — статус создания миниатюр (processing_status).
type ProcessingStatus string

const (
	ProcessingStatusPending    ProcessingStatus = "pending"
	ProcessingStatusProcessing ProcessingStatus = "processing"
	ProcessingStatusCompleted  ProcessingStatus = "completed"
	ProcessingStatusFailed     ProcessingStatus = "failed"
)

// Статусы аватарки в ответах API (поле status).
const (
	APIStatusProcessing = "processing"
	APIStatusReady      = "ready"
	APIStatusFailed     = "failed"
)

// Avatar — строка таблицы avatars.
type Avatar struct {
	ID               uuid.UUID
	UserID           string
	FileName         string
	MimeType         string
	SizeBytes        int64
	Width            int
	Height           int
	S3Key            string
	ThumbnailS3Keys  map[string]string
	UploadStatus     UploadStatus
	ProcessingStatus ProcessingStatus
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
}

// APIStatus переводит processing_status в статус API:
// pending и processing → processing, completed → ready, failed → failed.
func (a *Avatar) APIStatus() string {
	switch a.ProcessingStatus {
	case ProcessingStatusCompleted:
		return APIStatusReady
	case ProcessingStatusFailed:
		return APIStatusFailed
	default:
		return APIStatusProcessing
	}
}
