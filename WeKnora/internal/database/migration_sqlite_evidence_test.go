package database

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSQLiteEvidenceMigrationKeepsOldReferencesAndRetainsSnapshotsOnRollback(t *testing.T) {
	root := sqliteRepoRoot(t)
	chdirAndRestore(t, copySQLiteMigrationsThrough(t, root, 65))
	path := filepath.Join(t.TempDir(), "evidence.db")
	opts := MigrationOptions{SQLiteDBPath: path}
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", opts))
	db := openSQLiteDB(t, path)
	const prior = `[{"id":"actual-legacy-chunk","knowledge_id":"legacy-document"}]`
	_, err := db.Exec(`INSERT INTO messages(id,request_id,session_id,role,content,knowledge_references) VALUES('legacy-message','legacy-request','legacy-session','assistant','Saved original answer',?)`, prior)
	require.NoError(t, err)
	chdirAndRestore(t, root)
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", opts))
	var content, references string
	require.NoError(t, db.QueryRow(`SELECT content,knowledge_references FROM messages WHERE id='legacy-message'`).Scan(&content, &references))
	require.Equal(t, "Saved original answer", content)
	require.JSONEq(t, prior, references)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM message_evidence_records`).Scan(&count))
	require.Zero(t, count, "old chunk references must not be fabricated into exact typed proof")
	_, err = db.Exec(`INSERT INTO message_evidence_records(message_id,evidence_id,session_id,position,payload,payload_sha256) VALUES('legacy-message','ev_new','legacy-session',0,'{}','fixture-digest')`)
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(root, "migrations/sqlite/000066_message_evidence_records.down.sql"))
	require.NoError(t, err)
	_, err = db.Exec(string(down))
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM message_evidence_records`).Scan(&count))
	require.Equal(t, 1, count, "application rollback does not destroy historical source snapshots")
}
