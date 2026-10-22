package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const expectedPostgresMigrationVersion = 129

func TestImagePermissionMigrationVersionsAreUnique(t *testing.T) {
	root := sqliteRepoRoot(t)
	for _, dialect := range []string{"sqlite", "versioned"} {
		entries, err := os.ReadDir(filepath.Join(root, "migrations", dialect))
		require.NoError(t, err)
		seen := map[string]string{}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".up.sql") {
				continue
			}
			version, _, _ := strings.Cut(entry.Name(), "_")
			require.NotContains(t, seen, version, "%s migration version %s must be unique: %s / %s", dialect, version, seen[version], entry.Name())
			seen[version] = entry.Name()
		}
	}
}

func TestSQLiteImagePermissionMigrationLineages(t *testing.T) {
	root := sqliteRepoRoot(t)
	for _, lineage := range []string{"fresh", "main_image_42", "workbench_permissions_46"} {
		t.Run(lineage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "lineage.db")
			opts := MigrationOptions{SQLiteDBPath: path}
			var db *sql.DB
			if lineage != "fresh" {
				maxVersion := 46
				if lineage == "main_image_42" {
					maxVersion = 41
				}
				legacy := copySQLiteMigrationsThrough(t, root, maxVersion)
				if lineage == "main_image_42" {
					copyImageMigrationAt(t, root, legacy, "sqlite", "000047_image_generation_executions.up.sql", "000042_image_generation_executions.up.sql")
				}
				chdirAndRestore(t, legacy)
				require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", opts))
				db = openSQLiteDB(t, path)
				seedImagePermissionLineage(t, db)
				if lineage == "main_image_42" {
					seedImageExecution(t, db)
				} else {
					_, err := db.Exec(`UPDATE knowledge_bases SET access_policy='{"space_permission":"none","members":[]}' WHERE id='lineage-kb';
UPDATE research_workbenches SET source_sharing_approved=TRUE,source_sharing_approved_by='retained-owner',source_sharing_modules='["diagram"]',restricted_view_profiles='[]' WHERE id='lineage-workbench'`)
					require.NoError(t, err)
				}
			}
			chdirAndRestore(t, root)
			require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", opts))
			require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", opts), "the compatibility repair must be idempotent")
			if db == nil {
				db = openSQLiteDB(t, path)
			}
			version, dirty := sqliteMigrationState(t, db)
			require.Equal(t, expectedSQLiteMigrationVersion, version)
			require.False(t, dirty)
			for table, columns := range versionedSQLiteColumns {
				for _, column := range columns {
					require.True(t, sqliteColumnExists(t, db, table, column), table+"."+column)
				}
			}
			if lineage != "fresh" {
				assertImagePermissionLineageSentinels(t, db)
				if lineage == "main_image_42" {
					assertImageExecutionSentinel(t, db)
					var policy sql.NullString
					var approved bool
					require.NoError(t, db.QueryRow(`SELECT access_policy FROM knowledge_bases WHERE id='lineage-kb'`).Scan(&policy))
					require.False(t, policy.Valid, "legacy access must remain unchanged")
					require.NoError(t, db.QueryRow(`SELECT source_sharing_approved FROM research_workbenches WHERE id='lineage-workbench'`).Scan(&approved))
					require.False(t, approved, "catch-up must not grant source sharing")
				} else {
					var policy, owner, modules string
					var approved bool
					require.NoError(t, db.QueryRow(`SELECT access_policy FROM knowledge_bases WHERE id='lineage-kb'`).Scan(&policy))
					require.JSONEq(t, `{"space_permission":"none","members":[]}`, policy)
					require.NoError(t, db.QueryRow(`SELECT source_sharing_approved,source_sharing_approved_by,source_sharing_modules FROM research_workbenches WHERE id='lineage-workbench'`).Scan(&approved, &owner, &modules))
					require.True(t, approved)
					require.Equal(t, "retained-owner", owner)
					require.JSONEq(t, `["diagram"]`, modules)
				}
			}
		})
	}
}

func TestSQLiteImagePermissionRepairPreservesPartialPoliciesAndRollsBack(t *testing.T) {
	root := sqliteRepoRoot(t)
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial", true: "rollback"}[rollback], func(t *testing.T) {
			legacy := copySQLiteMigrationsThrough(t, root, 41)
			copyImageMigrationAt(t, root, legacy, "sqlite", "000047_image_generation_executions.up.sql", "000042_image_generation_executions.up.sql")
			chdirAndRestore(t, legacy)
			path := filepath.Join(t.TempDir(), "partial.db")
			opts := MigrationOptions{SQLiteDBPath: path}
			require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", opts))
			db := openSQLiteDB(t, path)
			seedImagePermissionLineage(t, db)
			if rollback {
				_, err := db.Exec(`DROP TABLE research_workbenches`)
				require.NoError(t, err)
			} else {
				_, err := db.Exec(`ALTER TABLE knowledge_bases ADD COLUMN access_policy TEXT;
UPDATE knowledge_bases SET access_policy='{"space_permission":"none","members":[]}' WHERE id='lineage-kb'`)
				require.NoError(t, err)
			}
			chdirAndRestore(t, root)
			err := RunMigrationsWithOptions("sqlite3://unused", opts)
			if rollback {
				require.ErrorContains(t, err, "image generation permission lineage")
				require.False(t, sqliteColumnExists(t, db, "knowledge_bases", "access_policy"), "earlier column additions must roll back")
				version, dirty := sqliteMigrationState(t, db)
				require.Equal(t, 42, version)
				require.False(t, dirty, "a failed preflight must not advance the migration ledger")
			} else {
				require.NoError(t, err)
				var policy string
				require.NoError(t, db.QueryRow(`SELECT access_policy FROM knowledge_bases WHERE id='lineage-kb'`).Scan(&policy))
				require.JSONEq(t, `{"space_permission":"none","members":[]}`, policy)
				require.True(t, sqliteColumnExists(t, db, "research_workbenches", "source_sharing_requested"))
			}
		})
	}
}

func TestPostgresImagePermissionMigrationLineageRepair(t *testing.T) {
	adminDSN := os.Getenv("WEKNORA_WORKBENCH_MIGRATION_TEST_POSTGRES_ADMIN_DSN")
	if adminDSN == "" {
		t.Skip("set WEKNORA_WORKBENCH_MIGRATION_TEST_POSTGRES_ADMIN_DSN for an isolated PostgreSQL image/permission lineage test")
	}
	root := sqliteRepoRoot(t)
	chdirAndRestore(t, root)
	dsn := disposablePostgresMigrationDSN(t, adminDSN)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE knowledge_bases(id TEXT PRIMARY KEY,name TEXT);
CREATE TABLE research_workbenches(id TEXT PRIMARY KEY,user_id TEXT,knowledge_base_id TEXT);
INSERT INTO knowledge_bases VALUES('lineage-kb','Retained library');
INSERT INTO research_workbenches VALUES('lineage-workbench','owner','lineage-kb')`)
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000129_image_generation_executions.up.sql"))
	require.NoError(t, err)
	_, err = db.Exec(string(body))
	require.NoError(t, err)
	seedImageExecution(t, db)
	require.NoError(t, reconcileImageGenerationPermissionLineage(context.Background(), dsn, MigrationOptions{}, 124))
	require.NoError(t, reconcileImageGenerationPermissionLineage(context.Background(), dsn, MigrationOptions{}, 124))
	var policy sql.NullString
	var approved bool
	require.NoError(t, db.QueryRow(`SELECT access_policy FROM knowledge_bases WHERE id='lineage-kb'`).Scan(&policy))
	require.False(t, policy.Valid)
	require.NoError(t, db.QueryRow(`SELECT source_sharing_approved FROM research_workbenches WHERE id='lineage-workbench'`).Scan(&approved))
	require.False(t, approved)
	_, err = db.Exec(string(body))
	require.NoError(t, err, "renumbered image migration must preserve an existing image table")
	assertImageExecutionSentinel(t, db)
}

func TestPostgresImagePermissionMigrationLineages(t *testing.T) {
	adminDSN := os.Getenv("WEKNORA_WORKBENCH_MIGRATION_TEST_POSTGRES_ADMIN_DSN")
	if adminDSN == "" {
		t.Skip("set WEKNORA_WORKBENCH_MIGRATION_TEST_POSTGRES_ADMIN_DSN for isolated PostgreSQL image/permission upgrade tests")
	}
	root := sqliteRepoRoot(t)
	for _, lineage := range []string{"fresh", "main_image_124", "workbench_permissions_128"} {
		t.Run(lineage, func(t *testing.T) {
			dsn := disposablePostgresMigrationDSN(t, adminDSN)
			db, err := sql.Open("postgres", dsn)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			if lineage != "fresh" {
				maxVersion := 128
				if lineage == "main_image_124" {
					maxVersion = 123
				}
				legacy := copyPostgresMigrationsThrough(t, root, maxVersion)
				if lineage == "main_image_124" {
					copyImageMigrationAt(t, root, legacy, "versioned", "000129_image_generation_executions.up.sql", "000124_image_generation_executions.up.sql")
				}
				chdirAndRestore(t, legacy)
				require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}))
				seedImagePermissionLineage(t, db)
				if lineage == "main_image_124" {
					seedImageExecution(t, db)
				} else {
					_, err = db.Exec(`UPDATE knowledge_bases SET access_policy='{"space_permission":"none","members":[]}'::jsonb WHERE id='lineage-kb'`)
					require.NoError(t, err)
				}
			}
			chdirAndRestore(t, root)
			require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}))
			require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}))
			var version int
			var dirty bool
			require.NoError(t, db.QueryRow(`SELECT version,dirty FROM schema_migrations`).Scan(&version, &dirty))
			require.Equal(t, expectedPostgresMigrationVersion, version)
			require.False(t, dirty)
			if lineage != "fresh" {
				assertImagePermissionLineageSentinels(t, db)
				var policy sql.NullString
				require.NoError(t, db.QueryRow(`SELECT access_policy FROM knowledge_bases WHERE id='lineage-kb'`).Scan(&policy))
				if lineage == "main_image_124" {
					require.False(t, policy.Valid)
					assertImageExecutionSentinel(t, db)
				} else {
					require.True(t, policy.Valid)
					require.JSONEq(t, `{"space_permission":"none","members":[]}`, policy.String)
				}
			}
		})
	}
}

func copyImageMigrationAt(t *testing.T, root, dest, dialect, source, target string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "migrations", dialect, source))
	require.NoError(t, err)
	body = []byte(strings.ReplaceAll(string(body), " IF NOT EXISTS", ""))
	require.NoError(t, os.WriteFile(filepath.Join(dest, "migrations", dialect, target), body, 0o600))
}

func seedImagePermissionLineage(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO sessions(id,tenant_id,title) VALUES('lineage-session',1,'Retained conversation');
INSERT INTO knowledge_bases(id,tenant_id,name,creator_id,embedding_model_id,summary_model_id) VALUES('lineage-kb',1,'Retained library','owner','','');
INSERT INTO research_workbenches(id,tenant_id,user_id,name,knowledge_base_id,idempotency_key,request_hash) VALUES('lineage-workbench',1,'owner','Retained workbench','lineage-kb','lineage-key','lineage-hash')`)
	require.NoError(t, err)
}

func seedImageExecution(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO image_generation_executions(id,tenant_id,session_id,message_id,tool_call_id,model_id,request_hash,status,resource_url) VALUES('retained-execution',1,'lineage-session','lineage-message','lineage-call','image-model','lineage-hash','succeeded','/retained-image.png')`)
	require.NoError(t, err)
}

func assertImagePermissionLineageSentinels(t *testing.T, db *sql.DB) {
	t.Helper()
	for query, want := range map[string]string{
		`SELECT title FROM sessions WHERE id='lineage-session'`:                      "Retained conversation",
		`SELECT name FROM knowledge_bases WHERE id='lineage-kb'`:                     "Retained library",
		`SELECT name FROM research_workbenches WHERE id='lineage-workbench'`:         "Retained workbench",
		`SELECT request_hash FROM research_workbenches WHERE id='lineage-workbench'`: "lineage-hash",
	} {
		var actual string
		require.NoError(t, db.QueryRow(query).Scan(&actual))
		require.Equal(t, want, actual)
	}
}

func assertImageExecutionSentinel(t *testing.T, db *sql.DB) {
	t.Helper()
	var resource string
	require.NoError(t, db.QueryRow(`SELECT resource_url FROM image_generation_executions WHERE id='retained-execution'`).Scan(&resource))
	require.Equal(t, "/retained-image.png", resource)
}
