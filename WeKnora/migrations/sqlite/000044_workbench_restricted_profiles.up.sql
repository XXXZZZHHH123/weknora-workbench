-- NULL preserves explicitly approved legacy module grants. Saving [] revokes
-- restricted pages without changing independent knowledge-base permissions.
ALTER TABLE research_workbenches ADD COLUMN restricted_view_profiles TEXT;
