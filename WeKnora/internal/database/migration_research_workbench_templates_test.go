package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/require"
)

func TestSQLiteWorkbenchTemplatesUpgradeAndRollback(t *testing.T) {
	root := sqliteRepoRoot(t)
	legacy := copySQLiteMigrationsThrough(t, root, 40)
	chdirAndRestore(t, legacy)
	path := filepath.Join(t.TempDir(), "templates.db")
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: path}))
	db := openSQLiteDB(t, path)
	_, err := db.Exec(`INSERT INTO sessions(id,tenant_id,title) VALUES('existing-session',1,'Existing conversation');
 INSERT INTO research_workbenches(id,tenant_id,user_id,name,knowledge_base_id,idempotency_key,request_hash)
 VALUES('existing-workbench',1,'owner','Existing research','kb','existing-key','existing-hash')`)
	require.NoError(t, err)
	chdirAndRestore(t, root)
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: path}))
	assertWorkbenchTemplateMigration(t, db)

	m, err := migrate.New("file://"+filepath.Join(root, "migrations", "sqlite"), "sqlite3://"+path)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })
	// Target the template migration by version: later migrations may exist.
	require.NoError(t, m.Migrate(40))
	require.False(t, sqliteColumnExists(t, db, "research_workbenches", "workspace_mode"))
	require.False(t, sqliteColumnExists(t, db, "sessions", "research_workbench_id"))
	require.False(t, sqliteTableExists(t, db, "research_workbench_onboarding"))
	var name, hash, title string
	require.NoError(t, db.QueryRow("SELECT name,request_hash FROM research_workbenches WHERE id='existing-workbench'").Scan(&name, &hash))
	require.Equal(t, "Existing research", name)
	require.Equal(t, "existing-hash", hash)
	require.NoError(t, db.QueryRow("SELECT title FROM sessions WHERE id='existing-session'").Scan(&title))
	require.Equal(t, "Existing conversation", title)
	require.NoError(t, m.Migrate(41))
	assertWorkbenchTemplateMigration(t, db)
	require.NoError(t, m.Up())
}

func TestPostgresWorkbenchTemplatesUpgradeAndRollback(t *testing.T) {
	adminDSN := os.Getenv("WEKNORA_WORKBENCH_MIGRATION_TEST_POSTGRES_ADMIN_DSN")
	if adminDSN == "" {
		t.Skip("set WEKNORA_WORKBENCH_MIGRATION_TEST_POSTGRES_ADMIN_DSN for isolated PostgreSQL template migration tests")
	}
	dsn := disposablePostgresMigrationDSN(t, adminDSN)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("CREATE TABLE sessions(id VARCHAR(36) PRIMARY KEY,tenant_id BIGINT,title TEXT)")
	require.NoError(t, err)
	root := sqliteRepoRoot(t)
	previous, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000119_workspace_navigation.up.sql"))
	require.NoError(t, err)
	_, err = db.Exec(string(previous))
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO sessions(id,tenant_id,title) VALUES('existing-session',1,'Existing conversation');
 INSERT INTO research_workbenches(id,tenant_id,user_id,name,knowledge_base_id,idempotency_key,request_hash)
 VALUES('existing-workbench',1,'owner','Existing research','kb','existing-key','existing-hash')`)
	require.NoError(t, err)
	up, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000123_research_workbench_templates.up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000123_research_workbench_templates.down.sql"))
	require.NoError(t, err)
	_, err = db.Exec(string(up))
	require.NoError(t, err)
	assertWorkbenchTemplateMigration(t, db)
	_, err = db.Exec(string(down))
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM research_workbenches WHERE id='existing-workbench' AND request_hash='existing-hash'").Scan(&count))
	require.Equal(t, 1, count)
	_, err = db.Exec(string(up))
	require.NoError(t, err)
	assertWorkbenchTemplateMigration(t, db)
}

func assertWorkbenchTemplateMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	var mode, template, hash, scope string
	var version int
	require.NoError(t, db.QueryRow("SELECT workspace_mode,template_id,template_version,request_hash FROM research_workbenches WHERE id='existing-workbench'").Scan(&mode, &template, &version, &hash))
	require.Equal(t, "legacy", mode)
	require.Empty(t, template)
	require.Zero(t, version)
	require.Equal(t, "existing-hash", hash)
	require.NoError(t, db.QueryRow("SELECT research_workbench_id FROM sessions WHERE id='existing-session'").Scan(&scope))
	require.Empty(t, scope)
	_, err := db.Exec(`INSERT INTO research_workbench_onboarding(tenant_id,workbench_id,user_id,status) VALUES(1,'existing-workbench','owner','dismissed');
 INSERT INTO research_workbench_onboarding(tenant_id,workbench_id,user_id,status) VALUES(1,'existing-workbench','another-user','active')`)
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM research_workbench_onboarding WHERE workbench_id='existing-workbench'").Scan(&count))
	require.Equal(t, 2, count)
}
