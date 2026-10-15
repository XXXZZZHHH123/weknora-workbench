package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/require"
)

// This opt-in suite creates its own disposable databases, never tables in the
// caller's database. The DSN must have CREATE DATABASE permission. For a local
// PostgreSQL without ParadeDB, include options=-c%20app.skip_embedding%3Dtrue.
func TestPostgresWorkbenchMigrationLineages(t *testing.T) {
	adminDSN := os.Getenv("WEKNORA_WORKBENCH_MIGRATION_TEST_POSTGRES_ADMIN_DSN")
	if adminDSN == "" {
		t.Skip("set WEKNORA_WORKBENCH_MIGRATION_TEST_POSTGRES_ADMIN_DSN for isolated PostgreSQL lineage tests")
	}
	root := sqliteRepoRoot(t)
	for _, version := range []int{0, 111, 112, 113, 118} {
		t.Run(fmt.Sprintf("version_%d", version), func(t *testing.T) {
			dsn := disposablePostgresMigrationDSN(t, adminDSN)
			db, err := sql.Open("postgres", dsn)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			if version != 0 {
				maxVersion := version
				if version < 118 {
					maxVersion = 110
				}
				legacy := copyPostgresMigrationsThrough(t, root, maxVersion)
				if version < 118 {
					for i, name := range []string{"000119_workspace_navigation.up.sql", "000120_knowledge_directories.up.sql", "000121_default_knowledge_base.up.sql"} {
						if i+111 > version {
							break
						}
						body, readErr := os.ReadFile(filepath.Join(root, "migrations", "versioned", name))
						require.NoError(t, readErr)
						require.NoError(t, os.WriteFile(filepath.Join(legacy, "migrations", "versioned", fmt.Sprintf("%06d_%s", i+111, name[7:])), body, 0600))
					}
				}
				m, openErr := migrate.New("file://"+filepath.Join(legacy, "migrations", "versioned"), dsn)
				require.NoError(t, openErr)
				require.NoError(t, m.Up())
				sourceErr, dbErr := m.Close()
				require.NoError(t, sourceErr)
				require.NoError(t, dbErr)
				if version < 118 {
					_, err = db.Exec(`INSERT INTO assistant_projects (id,tenant_id,user_id,name) VALUES ('retained-project',1,'owner','Retained PostgreSQL project');
     INSERT INTO research_workbenches (id,tenant_id,user_id,name,knowledge_base_id,idempotency_key,request_hash)
      VALUES ('retained-research',1,'owner','Retained PostgreSQL research','kb','key','hash');
     CREATE TABLE embeddings (id BIGINT PRIMARY KEY,knowledge_id TEXT,chunk_id TEXT);`)
					require.NoError(t, err)
					_, err = db.Exec(`INSERT INTO sessions (id,tenant_id,title) VALUES ('pg-session',1,'Retained session');
                        INSERT INTO assistant_project_sessions (tenant_id,user_id,session_id,project_id) VALUES (1,'owner','pg-session','retained-project')`)
					require.NoError(t, err)
					if version >= 112 {
						_, err = db.Exec(`INSERT INTO knowledge_folders (id,tenant_id,knowledge_base_id,path) VALUES ('pg-folder',1,'kb','retained/nested')`)
						require.NoError(t, err)
					}
					if version >= 113 {
						_, err = db.Exec(`INSERT INTO default_knowledge_base_bindings (tenant_id,kind,knowledge_base_id) VALUES (1,'macro','kb')`)
						require.NoError(t, err)
					}
				} else {
					_, err = db.Exec(`UPDATE model_catalog_configs SET version=9,overlay='{"providers":{"retained":{}}}' WHERE id=1`)
					require.NoError(t, err)
				}
				_, err = db.Exec(`INSERT INTO chunks (id,tenant_id,knowledge_id,knowledge_base_id,content,chunk_index,start_at,end_at,image_info)
    VALUES ('pg-image',1,'document','kb','retained',0,0,8,'[{"url":"/kept.png","caption":"Kept projection"}]')`)
				require.NoError(t, err)
			}
			chdirAndRestore(t, root)
			require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}))
			require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}))
			var got int
			var dirty bool
			require.NoError(t, db.QueryRow(`SELECT version,dirty FROM schema_migrations`).Scan(&got, &dirty))
			require.Equal(t, 122, got)
			require.False(t, dirty)
			for _, table := range []string{"assistant_projects", "assistant_project_sessions", "research_workbenches", "knowledge_folders", "default_knowledge_base_bindings", "model_catalog_configs", "chunk_images"} {
				var exists bool
				require.NoError(t, db.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists))
				require.True(t, exists, table)
			}
			if version != 0 {
				var value string
				require.NoError(t, db.QueryRow(`SELECT caption FROM chunk_images WHERE chunk_id='pg-image'`).Scan(&value))
				require.Equal(t, "Kept projection", value)
			}
			if version >= 111 && version <= 113 {
				var value string
				require.NoError(t, db.QueryRow(`SELECT name FROM assistant_projects WHERE id='retained-project'`).Scan(&value))
				require.Equal(t, "Retained PostgreSQL project", value)
				require.NoError(t, db.QueryRow(`SELECT name FROM research_workbenches WHERE id='retained-research'`).Scan(&value))
				require.Equal(t, "Retained PostgreSQL research", value)
				require.NoError(t, db.QueryRow(`SELECT session_id FROM assistant_project_sessions WHERE project_id='retained-project'`).Scan(&value))
				require.Equal(t, "pg-session", value)
				if version >= 112 {
					require.NoError(t, db.QueryRow(`SELECT path FROM knowledge_folders WHERE id='pg-folder'`).Scan(&value))
					require.Equal(t, "retained/nested", value)
				}
				if version >= 113 {
					require.NoError(t, db.QueryRow(`SELECT knowledge_base_id FROM default_knowledge_base_bindings WHERE tenant_id=1 AND kind='macro'`).Scan(&value))
					require.Equal(t, "kb", value)
				}
				for _, index := range []string{"idx_embeddings_knowledge_id", "idx_embeddings_chunk_id"} {
					var exists bool
					require.NoError(t, db.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, index).Scan(&exists))
					require.True(t, exists, index)
				}
			}
			if version == 118 {
				var value int
				var overlay string
				require.NoError(t, db.QueryRow(`SELECT version,overlay FROM model_catalog_configs WHERE id=1`).Scan(&value, &overlay))
				require.Equal(t, 9, value)
				require.JSONEq(t, `{"providers":{"retained":{}}}`, overlay)
			}
		})
	}
}

func disposablePostgresMigrationDSN(t *testing.T, adminDSN string) string {
	t.Helper()
	parsed, err := url.Parse(adminDSN)
	require.NoError(t, err)
	name := "weknora_lineage_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	admin, err := sql.Open("postgres", adminDSN)
	require.NoError(t, err)
	_, err = admin.Exec(`CREATE DATABASE "` + name + `"`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid<>pg_backend_pid()`, name)
		_, dropErr := admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`"`)
		require.NoError(t, dropErr)
		_ = admin.Close()
	})
	parsed.Path = "/" + name
	return parsed.String()
}

func copyPostgresMigrationsThrough(t *testing.T, root string, version int) string {
	t.Helper()
	dest := t.TempDir()
	target := filepath.Join(dest, "migrations", "versioned")
	require.NoError(t, os.MkdirAll(target, 0755))
	entries, err := os.ReadDir(filepath.Join(root, "migrations", "versioned"))
	require.NoError(t, err)
	for _, entry := range entries {
		name := entry.Name()
		prefix, _, ok := strings.Cut(name, "_")
		if !ok || !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		n, parseErr := strconv.Atoi(prefix)
		require.NoError(t, parseErr)
		if n > version {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(root, "migrations", "versioned", name))
		require.NoError(t, readErr)
		require.NoError(t, os.WriteFile(filepath.Join(target, name), body, 0600))
	}
	return dest
}
