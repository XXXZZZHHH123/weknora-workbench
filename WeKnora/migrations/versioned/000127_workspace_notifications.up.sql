CREATE TABLE workspace_notifications (
    id BIGSERIAL PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    recipient_user_id VARCHAR(36) NOT NULL,
    audit_log_id BIGINT NOT NULL,
    actor_user_id VARCHAR(36) NOT NULL,
    action VARCHAR(64) NOT NULL,
    target_user_id VARCHAR(36) NOT NULL DEFAULT '',
    target_id VARCHAR(64) NOT NULL DEFAULT '',
    details JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    read_at TIMESTAMPTZ NULL,
    CONSTRAINT uniq_workspace_notification UNIQUE (tenant_id, recipient_user_id, audit_log_id)
);
CREATE INDEX idx_workspace_notification_inbox ON workspace_notifications(tenant_id, recipient_user_id, id DESC);
