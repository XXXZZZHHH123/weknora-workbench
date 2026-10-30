package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Exercise the actual deployed workbench lineage before the new official
// release, rather than treating a successful fresh installation as an upgrade.
func TestSQLiteUpstreamReleasePreservesWorkbenchData(t *testing.T) {
	root := sqliteRepoRoot(t)
	chdirAndRestore(t, copySQLiteMigrationsThrough(t, root, 49))
	opts := MigrationOptions{SQLiteDBPath: filepath.Join(t.TempDir(), "upgrade.db")}
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", opts))
	db := openSQLiteDB(t, opts.SQLiteDBPath)
	seedUpstreamReleaseSentinels(t, db)
	chdirAndRestore(t, root)
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", opts))
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", opts))
	assertUpstreamReleaseSentinels(t, db, expectedSQLiteMigrationVersion)
}

func TestPostgresUpstreamReleasePreservesWorkbenchData(t *testing.T) {
	admin := os.Getenv("WEKNORA_WORKBENCH_MIGRATION_TEST_POSTGRES_ADMIN_DSN")
	if admin == "" {
		t.Skip("set the disposable PostgreSQL migration admin DSN")
	}
	root := sqliteRepoRoot(t)
	dsn := disposablePostgresMigrationDSN(t, admin)
	chdirAndRestore(t, copyPostgresMigrationsThrough(t, root, 131))
	require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}))
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	seedUpstreamReleaseSentinels(t, db)
	chdirAndRestore(t, root)
	require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}))
	require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}))
	assertUpstreamReleaseSentinels(t, db, expectedPostgresMigrationVersion)
}

func seedUpstreamReleaseSentinels(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO user_feedback (tenant_id,user_id,kind,content)
 VALUES (1,'upgrade-owner','suggestion','Retained feedback');
 INSERT INTO sessions (id,tenant_id,title) VALUES ('upgrade-session',1,'Retained conversation');
 INSERT INTO research_workbenches
 (id,tenant_id,user_id,name,knowledge_base_id,idempotency_key,request_hash,source_sharing_approved)
 VALUES ('upgrade-workbench',1,'upgrade-owner','Retained research','upgrade-kb','upgrade-key','upgrade-hash',false)`)
	require.NoError(t, err)
}

func assertUpstreamReleaseSentinels(t *testing.T, db *sql.DB, wantVersion int) {
	t.Helper()
	var version int
	var dirty, shared bool
	require.NoError(t, db.QueryRow(`SELECT version,dirty FROM schema_migrations`).Scan(&version, &dirty))
	require.Equal(t, wantVersion, version)
	require.False(t, dirty)
	var content, title, kind, ownerSource, name string
	require.NoError(t, db.QueryRow(`SELECT content FROM user_feedback WHERE user_id='upgrade-owner'`).Scan(&content))
	require.Equal(t, "Retained feedback", content)
	require.NoError(t, db.QueryRow(`SELECT title,kind,owner_auth_source FROM sessions WHERE id='upgrade-session'`).Scan(&title, &kind, &ownerSource))
	require.Equal(t, "Retained conversation", title)
	require.Empty(t, kind)
	require.Empty(t, ownerSource)
	require.NoError(t, db.QueryRow(`SELECT name,source_sharing_approved FROM research_workbenches WHERE id='upgrade-workbench'`).Scan(&name, &shared))
	require.Equal(t, "Retained research", name)
	require.False(t, shared, "an official upgrade must not grant research source sharing")
	for _, table := range []string{"agent_prompts", "automations", "agent_collaborations"} {
		var count int
		require.NoError(t, db.QueryRow("SELECT count(*) FROM "+table).Scan(&count))
	}
}
