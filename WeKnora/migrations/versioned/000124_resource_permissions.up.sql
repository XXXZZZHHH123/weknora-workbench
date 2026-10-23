-- NULL preserves the access scope of pre-existing resources.
ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS access_policy JSONB;
ALTER TABLE research_workbenches ADD COLUMN IF NOT EXISTS access_policy JSONB;
ALTER TABLE research_workbenches ADD COLUMN IF NOT EXISTS source_sharing_approved BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE research_workbenches ADD COLUMN IF NOT EXISTS source_sharing_approved_by VARCHAR(512) NOT NULL DEFAULT '';
ALTER TABLE research_workbenches ADD COLUMN IF NOT EXISTS source_sharing_requested BOOLEAN NOT NULL DEFAULT FALSE;
