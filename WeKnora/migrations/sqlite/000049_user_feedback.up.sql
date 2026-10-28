CREATE TABLE user_feedback (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    kind VARCHAR(16) NOT NULL CHECK (kind IN ('bug','suggestion')),
    content TEXT NOT NULL,
    page_url VARCHAR(2048) NOT NULL DEFAULT '',
    page_title VARCHAR(256) NOT NULL DEFAULT '',
    user_agent VARCHAR(512) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_user_feedback_status ON user_feedback(status, id DESC);
