package repository

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func notificationAuditDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "notifications.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.AuditLog{}, &types.TenantMember{}))
	require.NoError(t, db.Exec(`CREATE TABLE workspace_notifications (
id INTEGER PRIMARY KEY AUTOINCREMENT, tenant_id INTEGER NOT NULL, recipient_user_id TEXT NOT NULL,
audit_log_id INTEGER NOT NULL, actor_user_id TEXT NOT NULL, action TEXT NOT NULL,
target_user_id TEXT NOT NULL DEFAULT '', target_id TEXT NOT NULL DEFAULT '', details TEXT NOT NULL DEFAULT '{}',
created_at DATETIME NOT NULL, read_at DATETIME NULL,
UNIQUE(tenant_id,recipient_user_id,audit_log_id))`).Error)
	for _, member := range []types.TenantMember{
		{TenantID: 7, UserID: "actor", Role: types.TenantRoleAdmin, Status: types.TenantMemberStatusActive},
		{TenantID: 7, UserID: "owner", Role: types.TenantRoleOwner, Status: types.TenantMemberStatusActive},
		{TenantID: 7, UserID: "other-admin", Role: types.TenantRoleAdmin, Status: types.TenantMemberStatusActive},
		{TenantID: 7, UserID: "editor", Role: types.TenantRoleContributor, Status: types.TenantMemberStatusActive},
		{TenantID: 7, UserID: "suspended", Role: types.TenantRoleAdmin, Status: types.TenantMemberStatusSuspended},
		{TenantID: 8, UserID: "foreign-owner", Role: types.TenantRoleOwner, Status: types.TenantMemberStatusActive},
	} {
		require.NoError(t, db.Create(&member).Error)
	}
	return db
}

func TestAuditMembershipChangeNotifiesOnlyOtherActiveWorkspaceAdministrators(t *testing.T) {
	db := notificationAuditDB(t)
	event := &types.AuditLog{TenantID: 7, ActorUserID: "actor", ActorRole: "admin", Action: types.AuditActionMemberRoleChanged,
		TargetUserID: "editor", Outcome: types.AuditOutcomeSuccess, CreatedAt: time.Now()}
	require.NoError(t, NewAuditLogRepository(db).Create(context.Background(), event))
	var recipients []string
	require.NoError(t, db.Table("workspace_notifications").Order("recipient_user_id").Pluck("recipient_user_id", &recipients).Error)
	require.Equal(t, []string{"other-admin", "owner"}, recipients)
}

func TestAuditNotificationSkipsFailuresNonAdministratorsAndOtherActivity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		actor   string
		role    string
		action  types.AuditAction
		outcome types.AuditOutcome
	}{
		{"failed", "actor", "admin", types.AuditActionMemberRemoved, types.AuditOutcomeFailed},
		{"denied", "actor", "admin", types.AuditActionMemberRemoved, types.AuditOutcomeDenied},
		{"viewer", "editor", "viewer", types.AuditActionMemberAdded, types.AuditOutcomeSuccess},
		{"document", "actor", "admin", types.AuditActionKnowledgeUpdated, types.AuditOutcomeSuccess},
		{"removed actor", "no-longer-a-member", "admin", types.AuditActionInvitationSent, types.AuditOutcomeSuccess},
		{"system", "", "admin", types.AuditActionInvitationRevoked, types.AuditOutcomeSuccess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := notificationAuditDB(t)
			require.NoError(t, NewAuditLogRepository(db).Create(context.Background(), &types.AuditLog{
				TenantID: 7, ActorUserID: tc.actor, ActorRole: tc.role, Action: tc.action, Outcome: tc.outcome, CreatedAt: time.Now(),
			}))
			var count int64
			require.NoError(t, db.Table("workspace_notifications").Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestAuditNotificationIsIdempotentAndSanitizesDetails(t *testing.T) {
	db := notificationAuditDB(t)
	event := &types.AuditLog{TenantID: 7, ActorUserID: "actor", ActorRole: "admin", Action: types.AuditActionInvitationSent,
		TargetUserID: "editor", Outcome: types.AuditOutcomeSuccess, CreatedAt: time.Now(),
		Details: types.JSON(`{"role":"contributor","invitation_id":24,"token":"secret-token","url":"signed-url","content":"private-content"}`)}
	require.NoError(t, NewAuditLogRepository(db).Create(context.Background(), event))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return createWorkspaceAdminNotifications(tx, event) }))
	var rows []types.WorkspaceNotification
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 2)
	for _, row := range rows {
		require.Equal(t, event.ID, row.AuditLogID)
		require.JSONEq(t, `{"role":"contributor","invitation_id":24}`, string(row.Details))
	}
}

func TestAuditAndNotificationInsertAreAtomic(t *testing.T) {
	db := notificationAuditDB(t)
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("reject-notification-test", func(tx *gorm.DB) {
		if tx.Statement.Table == "workspace_notifications" {
			tx.AddError(errors.New("isolated notification storage failure"))
		}
	}))
	event := &types.AuditLog{TenantID: 7, ActorUserID: "actor", ActorRole: "admin", Action: types.AuditActionMemberRemoved,
		Outcome: types.AuditOutcomeSuccess, CreatedAt: time.Now()}
	require.ErrorContains(t, NewAuditLogRepository(db).Create(context.Background(), event), "isolated notification storage failure")
	var count int64
	require.NoError(t, db.Model(&types.AuditLog{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestAuditSelfDepartureNotifiesOnlyWithTrustedMatchingFormerMembership(t *testing.T) {
	for _, tc := range []struct {
		name   string
		caller types.Caller
		target string
		want   int
	}{
		{"trusted self departure", types.Caller{TenantID: 7, UserID: "actor", Role: types.TenantRoleAdmin}, "actor", 2},
		{"missing authenticated actor", types.Caller{}, "actor", 0},
		{"different actor", types.Caller{TenantID: 7, UserID: "owner", Role: types.TenantRoleOwner}, "actor", 0},
		{"different workspace", types.Caller{TenantID: 8, UserID: "actor", Role: types.TenantRoleAdmin}, "actor", 0},
		{"not self departure", types.Caller{TenantID: 7, UserID: "actor", Role: types.TenantRoleAdmin}, "editor", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := notificationAuditDB(t)
			require.NoError(t, db.Where("tenant_id = 7 AND user_id = ?", "actor").Delete(&types.TenantMember{}).Error)
			ctx := types.WithCaller(context.Background(), tc.caller)
			event := &types.AuditLog{TenantID: 7, ActorUserID: "actor", ActorRole: "admin", Action: types.AuditActionMemberLeft,
				TargetType: "tenant_member", TargetUserID: tc.target, Outcome: types.AuditOutcomeSuccess, CreatedAt: time.Now()}
			require.NoError(t, NewAuditLogRepository(db).Create(ctx, event))
			var count int64
			require.NoError(t, db.Table("workspace_notifications").Count(&count).Error)
			require.Equal(t, int64(tc.want), count)
		})
	}
}
