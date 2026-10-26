-- Обработанные worker'ом сообщения: повторная доставка того же message_id пропускается.
CREATE TABLE processed_messages (
    message_id VARCHAR(255) PRIMARY KEY,
    message_type VARCHAR(50) NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
