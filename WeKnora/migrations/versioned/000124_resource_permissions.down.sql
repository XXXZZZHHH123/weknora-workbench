ALTER TABLE research_workbenches DROP COLUMN IF EXISTS source_sharing_approved_by;
ALTER TABLE research_workbenches DROP COLUMN IF EXISTS source_sharing_approved;
ALTER TABLE research_workbenches DROP COLUMN IF EXISTS access_policy;
ALTER TABLE knowledge_bases DROP COLUMN IF EXISTS access_policy;
ALTER TABLE research_workbenches DROP COLUMN IF EXISTS source_sharing_requested;
