package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSQLiteRestrictedProfilesKeepLegacyApprovalAndFailClosedOnDowngrade(t *testing.T) {
	root := sqliteRepoRoot(t)
	legacy := copySQLiteMigrationsThrough(t, root, 43)
	chdirAndRestore(t, legacy)
	path := filepath.Join(t.TempDir(), "restricted-profiles.db")
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: path}))
	db := openSQLiteDB(t, path)
	_, err := db.Exec(`INSERT INTO research_workbenches(id,tenant_id,user_id,name,knowledge_base_id,idempotency_key,request_hash,source_sharing_approved,source_sharing_approved_by,source_sharing_modules)
 VALUES('legacy',1,'owner','Legacy','kb','legacy','legacy-hash',1,'source-owner','["diagram"]'),('profiled',1,'owner','Profiled','kb','profiled','profiled-hash',1,'source-owner','["diagram"]')`)
	require.NoError(t, err)
	up, err := os.ReadFile(filepath.Join(root, "migrations", "sqlite", "000044_workbench_restricted_profiles.up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(root, "migrations", "sqlite", "000044_workbench_restricted_profiles.down.sql"))
	require.NoError(t, err)
	_, err = db.Exec(string(up))
	require.NoError(t, err)
	var profiles sql.NullString
	var approved bool
	require.NoError(t, db.QueryRow("SELECT restricted_view_profiles,source_sharing_approved FROM research_workbenches WHERE id='legacy'").Scan(&profiles, &approved))
	require.False(t, profiles.Valid)
	require.True(t, approved)
	_, err = db.Exec(`UPDATE research_workbenches SET restricted_view_profiles='[{"id":"view","name":"View","modules":["diagram"],"member_ids":["a"],"all_members":false}]' WHERE id='profiled'`)
	require.NoError(t, err)
	_, err = db.Exec(string(down))
	require.NoError(t, err)
	require.False(t, sqliteColumnExists(t, db, "research_workbenches", "restricted_view_profiles"))
	require.NoError(t, db.QueryRow("SELECT source_sharing_approved FROM research_workbenches WHERE id='legacy'").Scan(&approved))
	require.True(t, approved)
	var approver string
	var modules sql.NullString
	require.NoError(t, db.QueryRow("SELECT source_sharing_approved,source_sharing_approved_by,source_sharing_modules FROM research_workbenches WHERE id='profiled'").Scan(&approved, &approver, &modules))
	require.False(t, approved)
	require.Empty(t, approver)
	require.False(t, modules.Valid)
	_, err = db.Exec(string(up))
	require.NoError(t, err)
	require.NoError(t, db.QueryRow("SELECT restricted_view_profiles,source_sharing_approved FROM research_workbenches WHERE id='profiled'").Scan(&profiles, &approved))
	require.False(t, profiles.Valid)
	require.False(t, approved)
}

func TestPostgresRestrictedProfilesKeepLegacyApprovalAndFailClosedOnDowngrade(t *testing.T) {
	adminDSN := os.Getenv("WEKNORA_WORKBENCH_MIGRATION_TEST_POSTGRES_ADMIN_DSN")
	if adminDSN == "" {
		t.Skip("set WEKNORA_WORKBENCH_MIGRATION_TEST_POSTGRES_ADMIN_DSN for an isolated PostgreSQL restricted-view migration test")
	}
	db, err := sql.Open("postgres", disposablePostgresMigrationDSN(t, adminDSN))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE research_workbenches(id TEXT PRIMARY KEY,source_sharing_approved BOOLEAN,source_sharing_approved_by TEXT,source_sharing_modules JSONB);
 INSERT INTO research_workbenches VALUES('legacy',true,'source-owner','["diagram"]'),('profiled',true,'source-owner','["diagram"]')`)
	require.NoError(t, err)
	root := sqliteRepoRoot(t)
	up, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000126_workbench_restricted_profiles.up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000126_workbench_restricted_profiles.down.sql"))
	require.NoError(t, err)
	_, err = db.Exec(string(up))
	require.NoError(t, err)
	var profiles sql.NullString
	var approved bool
	require.NoError(t, db.QueryRow("SELECT restricted_view_profiles,source_sharing_approved FROM research_workbenches WHERE id='legacy'").Scan(&profiles, &approved))
	require.False(t, profiles.Valid)
	require.True(t, approved)
	_, err = db.Exec(`UPDATE research_workbenches SET restricted_view_profiles='[]'::jsonb WHERE id='profiled'`)
	require.NoError(t, err)
	_, err = db.Exec(string(down))
	require.NoError(t, err)
	require.NoError(t, db.QueryRow("SELECT source_sharing_approved FROM research_workbenches WHERE id='legacy'").Scan(&approved))
	require.True(t, approved)
	require.NoError(t, db.QueryRow("SELECT source_sharing_approved FROM research_workbenches WHERE id='profiled'").Scan(&approved))
	require.False(t, approved)
	_, err = db.Exec(string(up))
	require.NoError(t, err)
	require.NoError(t, db.QueryRow("SELECT restricted_view_profiles,source_sharing_approved FROM research_workbenches WHERE id='profiled'").Scan(&profiles, &approved))
	require.False(t, profiles.Valid)
	require.False(t, approved)
}
