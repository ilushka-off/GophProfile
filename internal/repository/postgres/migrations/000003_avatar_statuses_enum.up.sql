-- Статусы принимают только значения из domain.UploadStatus и domain.ProcessingStatus:
-- ENUM не даст записать в эти колонки произвольную строку.
CREATE TYPE upload_status AS ENUM ('uploading', 'uploaded', 'failed');
CREATE TYPE processing_status AS ENUM ('pending', 'processing', 'completed', 'failed');

-- DEFAULT типа VARCHAR не приводится к ENUM автоматически, поэтому снимаем его
-- на время смены типа.
ALTER TABLE avatars
    ALTER COLUMN upload_status DROP DEFAULT,
    ALTER COLUMN processing_status DROP DEFAULT;

ALTER TABLE avatars
    ALTER COLUMN upload_status TYPE upload_status USING upload_status::upload_status,
    ALTER COLUMN processing_status TYPE processing_status USING processing_status::processing_status;

ALTER TABLE avatars
    ALTER COLUMN upload_status SET DEFAULT 'uploading',
    ALTER COLUMN processing_status SET DEFAULT 'pending';
