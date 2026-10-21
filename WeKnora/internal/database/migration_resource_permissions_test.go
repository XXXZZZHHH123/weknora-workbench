package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/require"
)

func TestPostgresResourcePermissionsPreserveLegacyScope(t *testing.T) {
	adminDSN := os.Getenv("WEKNORA_WORKBENCH_MIGRATION_TEST_POSTGRES_ADMIN_DSN")
	if adminDSN == "" {
		t.Skip("set WEKNORA_WORKBENCH_MIGRATION_TEST_POSTGRES_ADMIN_DSN for an isolated PostgreSQL ACL migration test")
	}
	dsn := disposablePostgresMigrationDSN(t, adminDSN)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE knowledge_bases(id TEXT PRIMARY KEY, name TEXT);
CREATE TABLE research_workbenches(id TEXT PRIMARY KEY, user_id TEXT, knowledge_base_id TEXT);
INSERT INTO knowledge_bases VALUES('old-kb','Retained');
INSERT INTO research_workbenches VALUES('old-wb','owner','old-kb')`)
	require.NoError(t, err)
	root := sqliteRepoRoot(t)
	up, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000124_resource_permissions.up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000124_resource_permissions.down.sql"))
	require.NoError(t, err)
	_, err = db.Exec(string(up))
	require.NoError(t, err)
	var policy sql.NullString
	var approved bool
	require.NoError(t, db.QueryRow("SELECT access_policy FROM knowledge_bases WHERE id='old-kb'").Scan(&policy))
	require.False(t, policy.Valid)
	require.NoError(t, db.QueryRow("SELECT source_sharing_approved FROM research_workbenches WHERE id='old-wb'").Scan(&approved))
	require.False(t, approved)
	_, err = db.Exec(`UPDATE knowledge_bases SET access_policy='{"space_permission":"none","members":[]}'::jsonb WHERE id='old-kb'`)
	require.NoError(t, err)
	_, err = db.Exec(string(down))
	require.NoError(t, err)
	var name string
	require.NoError(t, db.QueryRow("SELECT name FROM knowledge_bases WHERE id='old-kb'").Scan(&name))
	require.Equal(t, "Retained", name)
	_, err = db.Exec(string(up))
	require.NoError(t, err)
}

func TestSQLiteResourcePermissionsPreserveLegacyScope(t *testing.T) {
	root := sqliteRepoRoot(t)
	legacy := copySQLiteMigrationsThrough(t, root, 41)
	chdirAndRestore(t, legacy)
	path := filepath.Join(t.TempDir(), "resource-acl.db")
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: path}))
	db := openSQLiteDB(t, path)
	_, err := db.Exec(`INSERT INTO knowledge_bases(id,tenant_id,name,creator_id,embedding_model_id,summary_model_id) VALUES('old-kb',1,'Unchanged KB','backfilled-owner','','');
INSERT INTO research_workbenches(id,tenant_id,user_id,name,knowledge_base_id,idempotency_key,request_hash) VALUES('old-wb',1,'researcher','Unchanged workbench','old-kb','old-key','old-hash')`)
	require.NoError(t, err)
	chdirAndRestore(t, root)
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: path}))
	var kbPolicy, wbPolicy sql.NullString
	var name, creator, workbenchOwner string
	var approved, requested bool
	require.NoError(t, db.QueryRow(`SELECT name,creator_id,access_policy FROM knowledge_bases WHERE id='old-kb'`).Scan(&name, &creator, &kbPolicy))
	require.Equal(t, "Unchanged KB", name)
	require.Equal(t, "backfilled-owner", creator)
	require.False(t, kbPolicy.Valid, "migration must not privatize old libraries or assume the backfilled creator was their real owner")
	require.NoError(t, db.QueryRow(`SELECT user_id,access_policy,source_sharing_approved,source_sharing_requested FROM research_workbenches WHERE id='old-wb'`).Scan(&workbenchOwner, &wbPolicy, &approved, &requested))
	require.Equal(t, "researcher", workbenchOwner)
	require.False(t, wbPolicy.Valid)
	require.False(t, approved)
	require.False(t, requested)
	m, err := migrate.New("file://"+filepath.Join(root, "migrations", "sqlite"), "sqlite3://"+path)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })
	require.NoError(t, m.Steps(-1))
	require.False(t, sqliteColumnExists(t, db, "knowledge_bases", "access_policy"))
	require.NoError(t, m.Steps(1))
	require.NoError(t, db.QueryRow(`SELECT access_policy FROM knowledge_bases WHERE id='old-kb'`).Scan(&kbPolicy))
	require.False(t, kbPolicy.Valid)
}
