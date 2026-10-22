CREATE TABLE user_workbench_pins (
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(512) NOT NULL,
    workbench_id VARCHAR(36) NOT NULL REFERENCES research_workbenches(id) ON DELETE CASCADE,
    pinned_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tenant_id, user_id, workbench_id)
);
