package database

import (
	"fmt"
	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteMigrationsUpgradeReleasedWorkbenchLineage(t *testing.T) {
	root := sqliteRepoRoot(t)
	for _, version := range []int{32, 33, 34} {
		for _, rawPath := range []bool{true, false} {
			t.Run(fmt.Sprintf("v%d/raw_path_%t", version, rawPath), func(t *testing.T) {
				path := createReleasedSQLiteWorkbench(t, root, version)
				if rawPath {
					withSpaces := filepath.Join(filepath.Dir(path), "released workbench.db")
					require.NoError(t, os.Rename(path, withSpaces))
					path = withSpaces
				}
				db := openSQLiteDB(t, path)
				_, err := db.Exec(`INSERT INTO assistant_projects (id,tenant_id,user_id,name) VALUES ('project',1,'owner','Retained project');
    INSERT INTO sessions (id,tenant_id,title) VALUES ('session',1,'Retained conversation');
    INSERT INTO assistant_project_sessions (tenant_id,user_id,session_id,project_id) VALUES (1,'owner','session','project');
    INSERT INTO research_workbenches (id,tenant_id,user_id,name,knowledge_base_id,idempotency_key,request_hash)
    VALUES ('research',1,'owner','Retained research','kb','create-key','hash');
    INSERT INTO chunks (id,tenant_id,knowledge_id,knowledge_base_id,content,chunk_index,start_at,end_at,image_info)
    VALUES ('image-chunk',1,'document','kb','retained content',0,0,16,'[{"url":"/retained.png","caption":"retained caption"}]');`)
				require.NoError(t, err)
				if version >= 33 {
					_, err = db.Exec(`INSERT INTO knowledge_folders (id,tenant_id,knowledge_base_id,path) VALUES ('folder',1,'kb','retained/nested')`)
					require.NoError(t, err)
				}
				if version >= 34 {
					_, err = db.Exec(`INSERT INTO default_knowledge_base_bindings (tenant_id,kind,knowledge_base_id) VALUES (1,'macro','kb')`)
					require.NoError(t, err)
				}
				chdirAndRestore(t, root)
				opts := MigrationOptions{}
				dsn := "sqlite3://" + path
				if rawPath {
					opts.SQLiteDBPath = path
					dsn = "sqlite3://unused"
				}
				require.NoError(t, RunMigrationsWithOptions(dsn, opts))
				require.NoError(t, RunMigrationsWithOptions(dsn, opts))
				got, dirty := sqliteMigrationState(t, db)
				require.Equal(t, expectedSQLiteMigrationVersion, got)
				require.False(t, dirty)
				for _, table := range versionedSQLiteTables {
					require.True(t, sqliteTableExists(t, db, table), table)
				}
				for table, columns := range versionedSQLiteColumns {
					for _, col := range columns {
						require.True(t, sqliteColumnExists(t, db, table, col), table+"."+col)
					}
				}
				var value string
				for query, want := range map[string]string{
					`SELECT name FROM assistant_projects WHERE id='project'`:                       "Retained project",
					`SELECT session_id FROM assistant_project_sessions WHERE project_id='project'`: "session",
					`SELECT name FROM research_workbenches WHERE id='research'`:                    "Retained research",
					`SELECT caption FROM chunk_images WHERE chunk_id='image-chunk'`:                "retained caption",
				} {
					require.NoError(t, db.QueryRow(query).Scan(&value))
					require.Equal(t, want, value)
				}
				if version >= 33 {
					require.NoError(t, db.QueryRow(`SELECT path FROM knowledge_folders WHERE id='folder'`).Scan(&value))
					require.Equal(t, "retained/nested", value)
				}
				if version >= 34 {
					require.NoError(t, db.QueryRow(`SELECT knowledge_base_id FROM default_knowledge_base_bindings WHERE tenant_id=1 AND kind='macro'`).Scan(&value))
					require.Equal(t, "kb", value)
				}
			})
		}
	}
}

func TestSQLiteMigrationsUpgradeMainLineage(t *testing.T) {
	root := sqliteRepoRoot(t)
	for version := 31; version <= 37; version++ {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			legacy := copySQLiteMigrationsThrough(t, root, version)
			chdirAndRestore(t, legacy)
			path := filepath.Join(t.TempDir(), "main.db")
			opts := MigrationOptions{SQLiteDBPath: path}
			require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", opts))
			db := openSQLiteDB(t, path)
			_, err := db.Exec(`INSERT INTO sessions (id,tenant_id,title) VALUES ('main-session',1,'Main lineage retained')`)
			require.NoError(t, err)
			chdirAndRestore(t, root)
			require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", opts))
			var value string
			require.NoError(t, db.QueryRow(`SELECT title FROM sessions WHERE id='main-session'`).Scan(&value))
			require.Equal(t, "Main lineage retained", value)
			got, dirty := sqliteMigrationState(t, db)
			require.Equal(t, expectedSQLiteMigrationVersion, got)
			require.False(t, dirty)
			for _, table := range versionedSQLiteTables {
				require.True(t, sqliteTableExists(t, db, table), table)
			}
		})
	}
}

func TestSQLiteWorkbenchCompatibilityRejectsUnknownSchema(t *testing.T) {
	root := sqliteRepoRoot(t)
	for name, mutation := range map[string]string{
		"missing_column":                `ALTER TABLE research_workbenches DROP COLUMN request_hash`,
		"missing_unique_index":          `DROP INDEX idx_research_create_key`,
		"missing_table":                 `DROP TABLE assistant_project_sessions`,
		"incompatible_source_positions": `ALTER TABLE chunks ADD COLUMN source_locators INTEGER`,
	} {
		t.Run(name, func(t *testing.T) {
			path := createReleasedSQLiteWorkbench(t, root, 34)
			db := openSQLiteDB(t, path)
			_, err := db.Exec(mutation)
			require.NoError(t, err)
			chdirAndRestore(t, root)
			err = RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: path})
			require.ErrorContains(t, err, "workbench migration lineage")
			got, dirty := sqliteMigrationState(t, db)
			require.Equal(t, 34, got)
			require.False(t, dirty)
			require.False(t, sqliteTableExists(t, db, "chunk_images"))
		})
	}
}

func TestSQLiteWorkbenchCompatibilityRollsBackAndRejectsDirty(t *testing.T) {
	root := sqliteRepoRoot(t)
	t.Run("rollback", func(t *testing.T) {
		path := createReleasedSQLiteWorkbench(t, root, 34)
		db := openSQLiteDB(t, path)
		faultyRoot := copySQLiteMigrationsThrough(t, root, 40)
		broken := filepath.Join(faultyRoot, "migrations", "sqlite", "000033_chunk_source_locators.up.sql")
		body, err := os.ReadFile(broken)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(broken, append(body, []byte("\nSELECT missing_compatibility_function();\n")...), 0600))
		chdirAndRestore(t, faultyRoot)
		require.Error(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: path}))
		got, dirty := sqliteMigrationState(t, db)
		require.Equal(t, 34, got)
		require.False(t, dirty)
		require.False(t, sqliteTableExists(t, db, "chunk_images"))
		require.False(t, sqliteColumnExists(t, db, "chunks", "source_locators"))
	})
	t.Run("dirty", func(t *testing.T) {
		path := createReleasedSQLiteWorkbench(t, root, 34)
		db := openSQLiteDB(t, path)
		_, err := db.Exec(`UPDATE schema_migrations SET dirty=1`)
		require.NoError(t, err)
		chdirAndRestore(t, root)
		require.ErrorContains(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: path}), "dirty state")
		got, dirty := sqliteMigrationState(t, db)
		require.Equal(t, 34, got)
		require.True(t, dirty)
		require.False(t, sqliteTableExists(t, db, "chunk_images"))
	})
}

func createReleasedSQLiteWorkbench(t *testing.T, root string, version int) string {
	t.Helper()
	legacy := copySQLiteMigrationsThrough(t, root, 31)
	for i, name := range []string{"000038_workspace_navigation.up.sql", "000039_knowledge_directories.up.sql", "000040_default_knowledge_base.up.sql"} {
		if i+32 > version {
			break
		}
		body, err := os.ReadFile(filepath.Join(root, "migrations", "sqlite", name))
		require.NoError(t, err)
		body = []byte(strings.ReplaceAll(string(body), " IF NOT EXISTS", ""))
		filename := fmt.Sprintf("%06d_%s", i+32, name[7:])
		require.NoError(t, os.WriteFile(filepath.Join(legacy, "migrations", "sqlite", filename), body, 0600))
	}
	path := filepath.Join(t.TempDir(), "released-workbench.db")
	m, err := migrate.New("file://"+filepath.Join(legacy, "migrations", "sqlite"), "sqlite3://"+path)
	require.NoError(t, err)
	require.NoError(t, m.Up())
	sourceErr, dbErr := m.Close()
	require.NoError(t, sourceErr)
	require.NoError(t, dbErr)
	return path
}

func TestSQLiteWorkbenchV32RejectsPrematureSourceColumn(t *testing.T) {
	root := sqliteRepoRoot(t)
	path := createReleasedSQLiteWorkbench(t, root, 32)
	db := openSQLiteDB(t, path)
	_, err := db.Exec(`ALTER TABLE chunks ADD COLUMN source_locators TEXT`)
	require.NoError(t, err)
	chdirAndRestore(t, root)
	require.ErrorContains(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: path}), "workbench migration lineage")
	got, dirty := sqliteMigrationState(t, db)
	require.Equal(t, 32, got)
	require.False(t, dirty)
	require.False(t, sqliteTableExists(t, db, "chunk_images"))
}
