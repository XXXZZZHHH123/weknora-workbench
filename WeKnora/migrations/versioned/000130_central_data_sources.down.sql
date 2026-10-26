-- Old binaries understand only the compatibility primary source. Revoke the
-- legacy projection consent before dropping the per-source authorization data.
UPDATE research_workbenches SET source_sharing_approved=FALSE,source_sharing_approved_by='',source_sharing_modules='[]';
DROP TABLE research_workbench_source_bindings;
DROP TABLE central_datasets;
ALTER TABLE research_workbenches DROP COLUMN sources_revision;
ALTER TABLE knowledge_bases DROP COLUMN system_role;
