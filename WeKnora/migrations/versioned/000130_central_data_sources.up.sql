ALTER TABLE knowledge_bases ADD COLUMN system_role VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE research_workbenches ADD COLUMN sources_revision BIGINT NOT NULL DEFAULT 0;
CREATE TABLE central_datasets (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    kind VARCHAR(32) NOT NULL,
    name VARCHAR(128) NOT NULL,
    backing_knowledge_base_id VARCHAR(36) NOT NULL UNIQUE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(tenant_id,kind)
);
CREATE TABLE research_workbench_source_bindings (
    id VARCHAR(128) PRIMARY KEY,
    workbench_id VARCHAR(36) NOT NULL REFERENCES research_workbenches(id) ON DELETE CASCADE,
    source_type VARCHAR(32) NOT NULL CHECK(source_type IN ('knowledge_base','central_dataset')),
    source_id VARCHAR(36) NOT NULL,
    source_tenant_id BIGINT NOT NULL,
    name VARCHAR(128) NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    status VARCHAR(32) NOT NULL DEFAULT 'active' CHECK(status IN ('active','removed','deleted')),
    sharing_approved BOOLEAN NOT NULL DEFAULT FALSE,
    sharing_requested BOOLEAN NOT NULL DEFAULT FALSE,
    sharing_approved_by VARCHAR(512) NOT NULL DEFAULT '',
    sharing_modules JSONB NOT NULL DEFAULT '[]',
    sharing_version BIGINT NOT NULL DEFAULT 0,
    legacy BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(workbench_id,source_type,source_id)
);
CREATE INDEX idx_workbench_source_lookup ON research_workbench_source_bindings(source_type,source_id,status);
CREATE INDEX idx_workbench_source_owner ON research_workbench_source_bindings(workbench_id,status);

-- Preserve the backing identity and published indicator IDs; observations are not copied.
INSERT INTO central_datasets(id,tenant_id,kind,name,backing_knowledge_base_id)
SELECT kb.id,b.tenant_id,b.kind,kb.name,kb.id FROM default_knowledge_base_bindings b
JOIN knowledge_bases kb ON kb.id=b.knowledge_base_id AND kb.tenant_id=b.tenant_id
WHERE kb.deleted_at IS NULL;
UPDATE knowledge_bases SET system_role='central' WHERE id IN (SELECT backing_knowledge_base_id FROM central_datasets);
INSERT INTO research_workbench_source_bindings(id,workbench_id,source_type,source_id,source_tenant_id,name,enabled,status,sharing_approved,sharing_requested,sharing_approved_by,sharing_modules,legacy)
SELECT w.id,w.id,CASE WHEN c.id IS NULL THEN 'knowledge_base' ELSE 'central_dataset' END,
       COALESCE(c.id,kb.id),kb.tenant_id,kb.name,CASE WHEN kb.deleted_at IS NULL THEN TRUE ELSE FALSE END,
       CASE WHEN kb.deleted_at IS NULL THEN 'active' ELSE 'deleted' END,
       w.source_sharing_approved,w.source_sharing_requested,w.source_sharing_approved_by,COALESCE(w.source_sharing_modules,'[]'),TRUE
FROM research_workbenches w JOIN knowledge_bases kb ON kb.id=w.knowledge_base_id
LEFT JOIN central_datasets c ON c.backing_knowledge_base_id=kb.id AND c.tenant_id=w.tenant_id;
INSERT INTO research_workbench_source_bindings(id,workbench_id,source_type,source_id,source_tenant_id,name,enabled,status)
SELECT w.id || '-macro',w.id,'central_dataset',c.id,c.tenant_id,c.name,TRUE,'active'
FROM research_workbenches w JOIN central_datasets c ON c.tenant_id=w.tenant_id
WHERE c.backing_knowledge_base_id<>w.knowledge_base_id;
UPDATE research_workbenches SET sources_revision=1;
