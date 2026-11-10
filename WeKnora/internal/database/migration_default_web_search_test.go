package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSQLiteDefaultWebSearchMigrationPreservesChoices(t *testing.T) {
	db := openSQLiteDB(t, filepath.Join(t.TempDir(), "search.db"))
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	defer conn.Close()
	verifyDefaultWebSearchMigration(t, conn, "sqlite", "000067", "TEXT")
}

func TestPostgresDefaultWebSearchMigrationPreservesChoices(t *testing.T) {
	dsn := os.Getenv("WEKNORA_MIGRATION_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WEKNORA_MIGRATION_TEST_POSTGRES_DSN to test PostgreSQL migrations")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer db.Close()
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	defer conn.Close()
	schema := "search_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = conn.ExecContext(context.Background(), `CREATE SCHEMA "`+schema+`"; SET search_path TO "`+schema+`"`)
	require.NoError(t, err)
	defer conn.ExecContext(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`)
	verifyDefaultWebSearchMigration(t, conn, "versioned", "000150", "JSONB")
}

func verifyDefaultWebSearchMigration(t *testing.T, conn *sql.Conn, dir, version, jsonType string) {
	t.Helper()
	ctx := context.Background()
	_, err := conn.ExecContext(ctx, `CREATE TABLE tenants (
		id BIGINT PRIMARY KEY, status TEXT, web_search_config `+jsonType+`, deleted_at TIMESTAMP);
		CREATE TABLE web_search_providers (
		id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT, name TEXT, provider TEXT,
		description TEXT, parameters `+jsonType+`, is_default BOOLEAN,
		created_at TIMESTAMP, updated_at TIMESTAMP, deleted_at TIMESTAMP);
		INSERT INTO tenants(id,status,web_search_config) VALUES
		(1,'active',NULL), (2,'active','null'), (3,'active','{}'),
		(4,'active','{"max_results":3}'), (5,'active','{"provider":"bing"}'),
		(6,'active','{"api_key":"fixture-legacy-key"}'),
		(7,'active','{"proxy_url":"https://proxy.example.com"}'),
		(8,'active',NULL), (9,'active',NULL), (10,'active',NULL),
		(11,'active',NULL), (12,'suspended',NULL),
		(13,'active','{"provider":"  ","api_key":"  ","proxy_url":"  "}');
		UPDATE tenants SET deleted_at=CURRENT_TIMESTAMP WHERE id=11;
		INSERT INTO web_search_providers(id,tenant_id,name,provider,parameters,is_default) VALUES
		('existing-default',8,'Company search','bing','{"api_key":"fixture-owned-key"}',true),
		('existing-nondefault',9,'Manual choice','tavily','{}',false),
		('deleted-provider',10,'Removed search','keenable','{}',true);
		UPDATE web_search_providers SET deleted_at=CURRENT_TIMESTAMP WHERE tenant_id=10;`)
	require.NoError(t, err)
	root := sqliteRepoRoot(t)
	up, err := os.ReadFile(filepath.Join(root, "migrations", dir, version+"_default_web_search.up.sql"))
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = conn.ExecContext(ctx, string(up))
		require.NoError(t, err, "backfill is idempotent")
	}
	rows, err := conn.QueryContext(ctx, `SELECT id,tenant_id,provider,CAST(parameters AS TEXT),is_default
		FROM web_search_providers WHERE id NOT IN ('existing-default','existing-nondefault','deleted-provider') ORDER BY tenant_id`)
	require.NoError(t, err)
	var tenants []int64
	seen := map[string]bool{}
	for rows.Next() {
		var id, provider, parameters string
		var tenantID int64
		var isDefault bool
		require.NoError(t, rows.Scan(&id, &tenantID, &provider, &parameters, &isDefault))
		_, err := uuid.Parse(id)
		require.NoError(t, err)
		require.False(t, seen[id])
		seen[id] = true
		require.Equal(t, "keenable", provider)
		require.JSONEq(t, `{}`, parameters)
		require.True(t, isDefault)
		tenants = append(tenants, tenantID)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, []int64{1, 2, 3, 4}, tenants)
	var name, parameters string
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT name,CAST(parameters AS TEXT) FROM web_search_providers WHERE id='existing-default'`).Scan(&name, &parameters))
	require.Equal(t, "Company search", name)
	require.JSONEq(t, `{"api_key":"fixture-owned-key"}`, parameters)
	var active bool
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT is_default FROM web_search_providers WHERE id='existing-nondefault'`).Scan(&active))
	require.False(t, active)
	var tuning string
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT CAST(web_search_config AS TEXT) FROM tenants WHERE id=4`).Scan(&tuning))
	require.JSONEq(t, `{"max_results":3}`, tuning)
	down, err := os.ReadFile(filepath.Join(root, "migrations", dir, version+"_default_web_search.down.sql"))
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, string(down))
	require.NoError(t, err)
	var count int
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM web_search_providers`).Scan(&count))
	require.Equal(t, 7, count, "application rollback preserves workspace settings")
}
