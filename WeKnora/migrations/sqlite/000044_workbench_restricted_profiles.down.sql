-- Older versions cannot enforce member-specific page groups. Revoke those
-- delegated grants before dropping their configuration instead of widening it.
UPDATE research_workbenches
SET source_sharing_approved = FALSE, source_sharing_approved_by = '', source_sharing_modules = NULL
WHERE restricted_view_profiles IS NOT NULL;
ALTER TABLE research_workbenches DROP COLUMN restricted_view_profiles;
