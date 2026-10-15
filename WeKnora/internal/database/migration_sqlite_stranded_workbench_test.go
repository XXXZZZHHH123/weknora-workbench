package database

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// These databases were advanced by a release that skipped the upstream image
// and source-position migrations. Their clean ledger must not hide the gap.
func createStrandedSQLiteWorkbench(t *testing.T, root string, version int) string {
	t.Helper()
	path := createReleasedSQLiteWorkbench(t, root, 34)
	m, err := migrate.New("file://"+filepath.Join(root, "migrations", "sqlite"), "sqlite3://"+path)
	require.NoError(t, err)
	require.NoError(t, m.Migrate(uint(version)))
	sourceErr, dbErr := m.Close()
	require.NoError(t, sourceErr)
	require.NoError(t, dbErr)
	return path
}

func TestSQLiteMigrationsRepairStrandedWorkbench(t *testing.T) {
	root := sqliteRepoRoot(t)
	for _, version := range []int{35, 38, 40} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			path := createStrandedSQLiteWorkbench(t, root, version)
			db := openSQLiteDB(t, path)
			require.False(t, sqliteColumnExists(t, db, "chunks", "source_locators"))
			require.False(t, sqliteTableExists(t, db, "chunk_images"))
			_, err := db.Exec(`INSERT INTO chunks
  (id,tenant_id,knowledge_id,knowledge_base_id,content,chunk_index,start_at,end_at,image_info)
  VALUES ('retained',1,'document','kb','original content',0,0,16,
  '[{"url":"/original.png","caption":"original caption"}]')`)
			require.NoError(t, err)
			chdirAndRestore(t, root)
			opts := MigrationOptions{SQLiteDBPath: path}
			require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", opts))
			require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", opts))
			got, dirty := sqliteMigrationState(t, db)
			require.Equal(t, expectedSQLiteMigrationVersion, got)
			require.False(t, dirty)
			require.True(t, sqliteColumnExists(t, db, "chunks", "source_locators"))
			require.True(t, sqliteTableExists(t, db, "chunk_images"))
			var content, caption string
			require.NoError(t, db.QueryRow(`SELECT content FROM chunks WHERE id='retained'`).Scan(&content))
			require.Equal(t, "original content", content)
			require.NoError(t, db.QueryRow(`SELECT caption FROM chunk_images WHERE chunk_id='retained'`).Scan(&caption))
			require.Equal(t, "original caption", caption)

			// Exercise the same repository write that failed during TXT parsing,
			// without using an embedding provider or bypassing the write itself.
			gormDB, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := gormDB.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			chunk := &types.Chunk{ID: "new-chunk", TenantID: 1, KnowledgeID: "document", KnowledgeBaseID: "kb",
				Content: "fresh parser output", IsEnabled: true, ChunkType: types.ChunkTypeText,
				SourceLocators: types.SourceLocators{{Type: types.SourceLocatorText, Start: 0, End: 19}}}
			require.NoError(t, repository.NewChunkRepository(gormDB).CreateChunks(context.Background(), []*types.Chunk{chunk}))
			var saved types.Chunk
			require.NoError(t, gormDB.First(&saved, "id = ?", chunk.ID).Error)
			require.Equal(t, chunk.Content, saved.Content)
			require.Equal(t, chunk.SourceLocators, saved.SourceLocators)
		})
	}
}

func TestSQLiteStrandedWorkbenchRejectsUnknownSchema(t *testing.T) {
	root := sqliteRepoRoot(t)
	for name, mutation := range map[string]string{
		"missing_hash_column": `ALTER TABLE research_workbenches DROP COLUMN request_hash`,
		"missing_index":       `DROP INDEX idx_research_create_key`,
		"wrong_source_type":   `ALTER TABLE chunks ADD COLUMN source_locators INTEGER`,
	} {
		t.Run(name, func(t *testing.T) {
			path := createStrandedSQLiteWorkbench(t, root, 40)
			db := openSQLiteDB(t, path)
			_, err := db.Exec(mutation)
			require.NoError(t, err)
			chdirAndRestore(t, root)
			err = RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: path})
			require.ErrorContains(t, err, "workbench migration lineage")
			got, dirty := sqliteMigrationState(t, db)
			require.Equal(t, 40, got)
			require.False(t, dirty)
			require.False(t, sqliteTableExists(t, db, "chunk_images"))
		})
	}
}

func TestSQLiteStrandedWorkbenchRepairRollsBack(t *testing.T) {
	root := sqliteRepoRoot(t)
	path := createStrandedSQLiteWorkbench(t, root, 40)
	db := openSQLiteDB(t, path)
	faultyRoot := copySQLiteMigrationsThrough(t, root, 40)
	broken := filepath.Join(faultyRoot, "migrations", "sqlite", "000033_chunk_source_locators.up.sql")
	body, err := os.ReadFile(broken)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(broken, append(body, []byte("\nSELECT invalid_repair_function();\n")...), 0600))
	chdirAndRestore(t, faultyRoot)
	require.Error(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: path}))
	got, dirty := sqliteMigrationState(t, db)
	require.Equal(t, 40, got)
	require.False(t, dirty)
	require.False(t, sqliteColumnExists(t, db, "chunks", "source_locators"))
	require.False(t, sqliteTableExists(t, db, "chunk_images"))
}

func TestSQLiteHealthyWorkbenchSkipsRepairBackfill(t *testing.T) {
	root := sqliteRepoRoot(t)
	chdirAndRestore(t, root)
	path := filepath.Join(t.TempDir(), "healthy.db")
	opts := MigrationOptions{SQLiteDBPath: path}
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", opts))
	healthyRoot := copySQLiteMigrationsThrough(t, root, 40)
	imageMigration := filepath.Join(healthyRoot, "migrations", "sqlite", "000032_chunk_images.up.sql")
	require.NoError(t, os.WriteFile(imageMigration, []byte("SELECT healthy_database_must_not_replay_backfill();"), 0600))
	chdirAndRestore(t, healthyRoot)
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", opts))
}
