ALTER TABLE knowledge_bases ADD COLUMN access_policy TEXT;
ALTER TABLE research_workbenches ADD COLUMN access_policy TEXT;
ALTER TABLE research_workbenches ADD COLUMN source_sharing_approved BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE research_workbenches ADD COLUMN source_sharing_approved_by TEXT NOT NULL DEFAULT '';
ALTER TABLE research_workbenches ADD COLUMN source_sharing_requested BOOLEAN NOT NULL DEFAULT FALSE;
