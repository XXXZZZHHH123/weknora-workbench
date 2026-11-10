-- Match PostgreSQL: only initialize active workspaces with no search history.
INSERT INTO web_search_providers
    (id, tenant_id, name, provider, description, parameters, is_default, created_at, updated_at)
SELECT LOWER(HEX(RANDOMBLOB(4))) || '-' || LOWER(HEX(RANDOMBLOB(2))) || '-4' || SUBSTR(LOWER(HEX(RANDOMBLOB(2))), 2) || '-'
    || SUBSTR('89ab', 1 + ABS(RANDOM() % 4), 1) || SUBSTR(LOWER(HEX(RANDOMBLOB(2))), 2) || '-' || LOWER(HEX(RANDOMBLOB(6))),
    t.id, 'Keenable', 'keenable', 'Public web search', '{}', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
FROM tenants t
WHERE t.deleted_at IS NULL AND t.status = 'active'
  AND NOT EXISTS (SELECT 1 FROM web_search_providers p WHERE p.tenant_id = t.id)
  AND COALESCE(JSON_EXTRACT(t.web_search_config, '$.provider'), '') = ''
  AND COALESCE(JSON_EXTRACT(t.web_search_config, '$.api_key'), '') = ''
  AND COALESCE(JSON_EXTRACT(t.web_search_config, '$.proxy_url'), '') = '';
