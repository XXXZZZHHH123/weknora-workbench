-- Only workspaces that have never configured search receive a public default.
-- Keep legacy credentials/proxies, deleted-provider choices and agent bindings.
BEGIN;
LOCK TABLE tenants, web_search_providers IN SHARE ROW EXCLUSIVE MODE;
INSERT INTO web_search_providers
    (id, tenant_id, name, provider, description, parameters, is_default, created_at, updated_at)
SELECT gen_random_uuid()::text, t.id, 'Keenable', 'keenable', 'Public web search', '{}'::jsonb, true, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
FROM tenants t
WHERE t.deleted_at IS NULL AND t.status = 'active'
  AND NOT EXISTS (SELECT 1 FROM web_search_providers p WHERE p.tenant_id = t.id)
  AND COALESCE(t.web_search_config->>'provider', '') = ''
  AND COALESCE(t.web_search_config->>'api_key', '') = ''
  AND COALESCE(t.web_search_config->>'proxy_url', '') = '';
COMMIT;
