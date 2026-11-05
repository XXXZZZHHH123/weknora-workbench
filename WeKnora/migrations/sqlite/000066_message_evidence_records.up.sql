CREATE TABLE IF NOT EXISTS message_evidence_records (
    message_id VARCHAR(36) NOT NULL,
    evidence_id VARCHAR(128) NOT NULL,
    session_id VARCHAR(36) NOT NULL,
    position INTEGER NOT NULL,
    payload TEXT NOT NULL,
    payload_sha256 VARCHAR(64) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (message_id, evidence_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_message_evidence_position ON message_evidence_records (message_id, position);
CREATE INDEX IF NOT EXISTS idx_message_evidence_session ON message_evidence_records (session_id, message_id);
