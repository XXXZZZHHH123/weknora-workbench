package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func centralFixtureSchema(t *testing.T, db *sql.DB, boolean, timestamp string) {
	t.Helper()
	_, err := db.Exec(`CREATE TABLE knowledge_bases(id VARCHAR(36) PRIMARY KEY,tenant_id BIGINT NOT NULL,name VARCHAR(128) NOT NULL,deleted_at ` + timestamp + `);
CREATE TABLE default_knowledge_base_bindings(tenant_id BIGINT NOT NULL,kind VARCHAR(32) NOT NULL,knowledge_base_id VARCHAR(36) NOT NULL);
CREATE TABLE research_workbenches(id VARCHAR(36) PRIMARY KEY,tenant_id BIGINT NOT NULL,knowledge_base_id VARCHAR(36) NOT NULL,source_sharing_approved ` + boolean + ` NOT NULL,source_sharing_requested ` + boolean + ` NOT NULL,source_sharing_approved_by VARCHAR(512),source_sharing_modules TEXT);
INSERT INTO knowledge_bases(id,tenant_id,name)VALUES('ordinary',7,'Ordinary'),('default',7,'Renamed center'),('same-name',7,'Renamed center');
INSERT INTO default_knowledge_base_bindings VALUES(7,'macro','default');`)
	require.NoError(t, err)
}

func assertCentralPreservesBindings(t *testing.T, db *sql.DB) {
	t.Helper()
	var role, id string
	require.NoError(t, db.QueryRow("SELECT system_role FROM knowledge_bases WHERE id='default'").Scan(&role))
	require.Equal(t, "central", role)
	require.NoError(t, db.QueryRow("SELECT system_role FROM knowledge_bases WHERE id='same-name'").Scan(&role))
	require.Empty(t, role)
	require.NoError(t, db.QueryRow("SELECT backing_knowledge_base_id FROM central_datasets").Scan(&id))
	require.Equal(t, "default", id)
	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM research_workbench_source_bindings WHERE workbench_id='ordinary-wb'").Scan(&count))
	require.Equal(t, 2, count)
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM research_workbench_source_bindings WHERE workbench_id='center-wb'").Scan(&count))
	require.Equal(t, 1, count)
	var approved bool
	var modules string
	require.NoError(t, db.QueryRow("SELECT sharing_approved,sharing_modules FROM research_workbench_source_bindings WHERE id='ordinary-wb'").Scan(&approved, &modules))
	require.True(t, approved)
	require.JSONEq(t, `["diagram"]`, modules)
}

func TestSQLiteCentralSourcesMigrationKeepsDefaultIDsAndLegacyConsent(t *testing.T) {
	db := openSQLiteDB(t, filepath.Join(t.TempDir(), "central.db"))
	centralFixtureSchema(t, db, "BOOLEAN", "DATETIME")
	_, err := db.Exec(`INSERT INTO research_workbenches VALUES('ordinary-wb',7,'ordinary',1,1,'source-owner','["diagram"]'),('center-wb',7,'default',0,0,'','[]')`)
	require.NoError(t, err)
	root := sqliteRepoRoot(t)
	up, err := os.ReadFile(filepath.Join(root, "migrations/sqlite/000048_central_data_sources.up.sql"))
	require.NoError(t, err)
	_, err = db.Exec(string(up))
	require.NoError(t, err)
	assertCentralPreservesBindings(t, db)
	var revision int
	require.NoError(t, db.QueryRow("SELECT sources_revision FROM research_workbenches WHERE id='ordinary-wb'").Scan(&revision))
	require.Equal(t, 1, revision)
}

func TestPostgresCentralSourcesMigrationKeepsDefaultIDsAndLegacyConsent(t *testing.T) {
	admin := os.Getenv("WEKNORA_WORKBENCH_MIGRATION_TEST_POSTGRES_ADMIN_DSN")
	if admin == "" {
		t.Skip("set WEKNORA_WORKBENCH_MIGRATION_TEST_POSTGRES_ADMIN_DSN for isolated PostgreSQL migration tests")
	}
	db, err := sql.Open("postgres", disposablePostgresMigrationDSN(t, admin))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	centralFixtureSchema(t, db, "BOOLEAN", "TIMESTAMP WITH TIME ZONE")
	_, err = db.Exec(`ALTER TABLE research_workbenches ALTER COLUMN source_sharing_modules TYPE JSONB USING source_sharing_modules::jsonb;
INSERT INTO research_workbenches VALUES('ordinary-wb',7,'ordinary',TRUE,TRUE,'source-owner','["diagram"]'),('center-wb',7,'default',FALSE,FALSE,'','[]')`)
	require.NoError(t, err)
	up, err := os.ReadFile(filepath.Join(sqliteRepoRoot(t), "migrations/versioned/000130_central_data_sources.up.sql"))
	require.NoError(t, err)
	_, err = db.Exec(string(up))
	require.NoError(t, err)
	assertCentralPreservesBindings(t, db)
}
