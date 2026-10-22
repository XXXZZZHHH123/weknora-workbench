CREATE TABLE workspace_notifications (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id INTEGER NOT NULL,
    recipient_user_id TEXT NOT NULL,
    audit_log_id INTEGER NOT NULL,
    actor_user_id TEXT NOT NULL,
    action TEXT NOT NULL,
    target_user_id TEXT NOT NULL DEFAULT '',
    target_id TEXT NOT NULL DEFAULT '',
    details TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    read_at DATETIME NULL,
    CONSTRAINT uniq_workspace_notification UNIQUE (tenant_id, recipient_user_id, audit_log_id)
);
CREATE INDEX idx_workspace_notification_inbox ON workspace_notifications(tenant_id, recipient_user_id, id DESC);
