package database

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSQLiteWorkspaceNotificationMigrationPreservesAuditAndEnforcesRecipientUniqueness(t *testing.T) {
	root := sqliteRepoRoot(t)
	db := openSQLiteDB(t, filepath.Join(t.TempDir(), "notifications.db"))
	_, err := db.Exec(`CREATE TABLE audit_logs(id INTEGER PRIMARY KEY, actor_user_id TEXT);
INSERT INTO audit_logs VALUES(3,'actual-actor')`)
	require.NoError(t, err)
	up, err := os.ReadFile(filepath.Join(root, "migrations", "sqlite", "000045_workspace_notifications.up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(root, "migrations", "sqlite", "000045_workspace_notifications.down.sql"))
	require.NoError(t, err)
	_, err = db.Exec(string(up))
	require.NoError(t, err)
	insert := `INSERT INTO workspace_notifications(tenant_id,recipient_user_id,audit_log_id,actor_user_id,action) VALUES(7,'owner',3,'actual-actor','rbac.member_added')`
	_, err = db.Exec(insert)
	require.NoError(t, err)
	_, err = db.Exec(insert)
	require.Error(t, err, "one audit event must create at most one notification per recipient")
	_, err = db.Exec(`INSERT INTO workspace_notifications(tenant_id,recipient_user_id,audit_log_id,actor_user_id,action) VALUES(8,'owner',3,'actual-actor','rbac.member_added')`)
	require.NoError(t, err, "identical recipients in another workspace remain separate")
	_, err = db.Exec(string(down))
	require.NoError(t, err)
	var actor string
	require.NoError(t, db.QueryRow("SELECT actor_user_id FROM audit_logs WHERE id=3").Scan(&actor))
	require.Equal(t, "actual-actor", actor)
	_, err = db.Exec(string(up))
	require.NoError(t, err)
}
