-- Retain immutable evidence on application rollback; use a forward migration
-- for any later storage change rather than silently deleting proof.
SELECT 1;
