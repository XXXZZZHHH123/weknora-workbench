CREATE TABLE assistant_projects (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    user_id VARCHAR(512) NOT NULL,
    name VARCHAR(128) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_assistant_project_owner ON assistant_projects (tenant_id, user_id);

CREATE TABLE assistant_project_sessions (
    tenant_id BIGINT NOT NULL,
    user_id VARCHAR(512) NOT NULL,
    session_id VARCHAR(36) NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id VARCHAR(36) NOT NULL REFERENCES assistant_projects(id) ON DELETE CASCADE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tenant_id, user_id, session_id)
);
CREATE INDEX idx_assistant_project_sessions_project_id ON assistant_project_sessions (project_id);

-- An entry into the existing tenant research service, not a new fact database.
CREATE TABLE research_workbenches (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    user_id VARCHAR(512) NOT NULL,
    name VARCHAR(128) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    knowledge_base_id VARCHAR(36) NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    request_hash VARCHAR(64) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP WITH TIME ZONE
);
CREATE UNIQUE INDEX idx_research_create_key ON research_workbenches (tenant_id, user_id, idempotency_key);
CREATE INDEX idx_research_workbenches_tenant_id ON research_workbenches (tenant_id);
CREATE INDEX idx_research_workbenches_knowledge_base_id ON research_workbenches (knowledge_base_id);
CREATE INDEX idx_research_workbenches_deleted_at ON research_workbenches (deleted_at);
