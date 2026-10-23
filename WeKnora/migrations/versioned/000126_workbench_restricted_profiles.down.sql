UPDATE research_workbenches
SET source_sharing_approved = FALSE, source_sharing_approved_by = '', source_sharing_modules = NULL
WHERE restricted_view_profiles IS NOT NULL;
ALTER TABLE research_workbenches DROP COLUMN IF EXISTS restricted_view_profiles;
