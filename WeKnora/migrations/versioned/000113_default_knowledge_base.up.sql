CREATE TABLE default_knowledge_base_bindings (
    tenant_id BIGINT NOT NULL,
    kind VARCHAR(32) NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL UNIQUE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tenant_id, kind)
);
