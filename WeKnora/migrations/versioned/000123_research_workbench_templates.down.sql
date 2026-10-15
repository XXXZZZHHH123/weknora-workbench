DROP TABLE research_workbench_onboarding;
DROP INDEX idx_sessions_research_workbench_id;
ALTER TABLE sessions DROP COLUMN research_workbench_id;
ALTER TABLE research_workbenches DROP COLUMN workspace_mode;
ALTER TABLE research_workbenches DROP COLUMN template_version;
ALTER TABLE research_workbenches DROP COLUMN template_id;
