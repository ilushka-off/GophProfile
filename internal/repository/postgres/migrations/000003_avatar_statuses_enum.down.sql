ALTER TABLE avatars
    ALTER COLUMN upload_status DROP DEFAULT,
    ALTER COLUMN processing_status DROP DEFAULT;

ALTER TABLE avatars
    ALTER COLUMN upload_status TYPE VARCHAR(50) USING upload_status::text,
    ALTER COLUMN processing_status TYPE VARCHAR(50) USING processing_status::text;

ALTER TABLE avatars
    ALTER COLUMN upload_status SET DEFAULT 'uploading',
    ALTER COLUMN processing_status SET DEFAULT 'pending';

DROP TYPE IF EXISTS processing_status;
DROP TYPE IF EXISTS upload_status;
