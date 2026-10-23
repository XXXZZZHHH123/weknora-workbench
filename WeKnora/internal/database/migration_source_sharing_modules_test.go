package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/require"
)

func TestSQLiteSourceSharingModulesPreserveLegacyApprovalWithoutGrantingPages(t *testing.T) {
	root := sqliteRepoRoot(t)
	legacy := copySQLiteMigrationsThrough(t, root, 42)
	chdirAndRestore(t, legacy)
	path := filepath.Join(t.TempDir(), "source-modules.db")
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: path}))
	db := openSQLiteDB(t, path)
	_, err := db.Exec(`INSERT INTO research_workbenches(id,tenant_id,user_id,name,knowledge_base_id,idempotency_key,request_hash,source_sharing_approved,source_sharing_approved_by)
VALUES('old-wb',1,'researcher','Unchanged workbench','old-kb','old-key','old-hash',1,'source-owner')`)
	require.NoError(t, err)
	chdirAndRestore(t, root)
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: path}))
	var modules sql.NullString
	var approved bool
	var owner string
	require.NoError(t, db.QueryRow("SELECT source_sharing_modules,source_sharing_approved,source_sharing_approved_by FROM research_workbenches WHERE id='old-wb'").Scan(&modules, &approved, &owner))
	require.False(t, modules.Valid, "an old approval must not implicitly grant every page")
	require.True(t, approved)
	require.Equal(t, "source-owner", owner)
	_, err = db.Exec(`UPDATE research_workbenches SET source_sharing_modules='["market","diagram"]' WHERE id='old-wb'`)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow("SELECT source_sharing_modules FROM research_workbenches WHERE id='old-wb'").Scan(&modules))
	require.JSONEq(t, `["market","diagram"]`, modules.String)
	m, err := migrate.New("file://"+filepath.Join(root, "migrations", "sqlite"), "sqlite3://"+path)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })
	if version, _, err := m.Version(); err == nil && version != 43 {
		require.NoError(t, m.Migrate(43))
	}
	require.NoError(t, m.Steps(-1))
	require.False(t, sqliteColumnExists(t, db, "research_workbenches", "source_sharing_modules"))
	require.NoError(t, m.Steps(1))
	require.NoError(t, db.QueryRow("SELECT source_sharing_modules FROM research_workbenches WHERE id='old-wb'").Scan(&modules))
	require.False(t, modules.Valid)
}

func TestPostgresSourceSharingModulesPreserveLegacyApprovalWithoutGrantingPages(t *testing.T) {
	adminDSN := os.Getenv("WEKNORA_WORKBENCH_MIGRATION_TEST_POSTGRES_ADMIN_DSN")
	if adminDSN == "" {
		t.Skip("set WEKNORA_WORKBENCH_MIGRATION_TEST_POSTGRES_ADMIN_DSN for an isolated PostgreSQL source-sharing migration test")
	}
	db, err := sql.Open("postgres", disposablePostgresMigrationDSN(t, adminDSN))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE research_workbenches(id TEXT PRIMARY KEY, source_sharing_approved BOOLEAN, source_sharing_approved_by TEXT);
INSERT INTO research_workbenches VALUES('old-wb',true,'source-owner')`)
	require.NoError(t, err)
	root := sqliteRepoRoot(t)
	up, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000125_source_sharing_modules.up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000125_source_sharing_modules.down.sql"))
	require.NoError(t, err)
	_, err = db.Exec(string(up))
	require.NoError(t, err)
	var modules sql.NullString
	var approved bool
	require.NoError(t, db.QueryRow("SELECT source_sharing_modules,source_sharing_approved FROM research_workbenches WHERE id='old-wb'").Scan(&modules, &approved))
	require.False(t, modules.Valid)
	require.True(t, approved)
	_, err = db.Exec(`UPDATE research_workbenches SET source_sharing_modules='["diagram"]'::jsonb WHERE id='old-wb'`)
	require.NoError(t, err)
	_, err = db.Exec(string(down))
	require.NoError(t, err)
	_, err = db.Exec(string(up))
	require.NoError(t, err)
	require.NoError(t, db.QueryRow("SELECT source_sharing_modules FROM research_workbenches WHERE id='old-wb'").Scan(&modules))
	require.False(t, modules.Valid)
}
