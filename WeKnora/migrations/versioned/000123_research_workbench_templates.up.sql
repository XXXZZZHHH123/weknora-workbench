ALTER TABLE research_workbenches ADD COLUMN template_id VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE research_workbenches ADD COLUMN template_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE research_workbenches ADD COLUMN workspace_mode VARCHAR(16) NOT NULL DEFAULT 'legacy' CHECK (workspace_mode IN ('legacy', 'isolated'));
ALTER TABLE sessions ADD COLUMN research_workbench_id VARCHAR(36) NOT NULL DEFAULT '';
CREATE INDEX idx_sessions_research_workbench_id ON sessions (research_workbench_id);

CREATE TABLE research_workbench_onboarding (
    tenant_id BIGINT NOT NULL,
    workbench_id VARCHAR(36) NOT NULL REFERENCES research_workbenches(id) ON DELETE CASCADE,
    user_id VARCHAR(512) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'dismissed', 'skipped', 'completed')),
    task_sources BOOLEAN NOT NULL DEFAULT FALSE,
    task_review BOOLEAN NOT NULL DEFAULT FALSE,
    task_research BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tenant_id, workbench_id, user_id)
);
