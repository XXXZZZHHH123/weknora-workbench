-- Both migration lineages own these shared schemas; a rollback must keep
-- their data and leave the upstream migrations responsible for their removal.
SELECT 1;
