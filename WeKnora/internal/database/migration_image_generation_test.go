package database

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageGenerationMigrationEnforcesOneSubmissionPerMessage(t *testing.T) {
	root := sqliteRepoRoot(t)
	chdirAndRestore(t, root)
	path := filepath.Join(t.TempDir(), "image-generation.db")
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: path}))
	db := openSQLiteDB(t, path)
	insert := `INSERT INTO image_generation_executions (id,tenant_id,session_id,message_id,tool_call_id,model_id,request_hash,status) VALUES (?,1,'session',?,?,'image-model','hash','started')`
	_, err := db.Exec(insert, "execution-1", "message-1", "call-1")
	require.NoError(t, err)
	_, err = db.Exec(insert, "execution-2", "message-1", "call-2")
	require.Error(t, err, "new tool IDs cannot cause a second paid submission in one reply")
	_, err = db.Exec(insert, "execution-3", "message-2", "call-3")
	require.NoError(t, err, "a new user turn has a new assistant reply budget")
}
