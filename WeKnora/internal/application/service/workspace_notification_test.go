package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func workspaceNotificationFixture(t *testing.T) (*gorm.DB, *WorkspaceNotificationService, context.Context) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "inbox.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.WorkspaceNotification{}, &types.TenantMember{}))
	require.NoError(t, db.Exec(`CREATE TABLE users(id TEXT PRIMARY KEY, username TEXT);
INSERT INTO users VALUES('actor','Actual operator'),('foreign','Foreign operator')`).Error)
	for _, member := range []types.TenantMember{
		{TenantID: 7, UserID: "reader", Role: types.TenantRoleAdmin, Status: types.TenantMemberStatusActive},
		{TenantID: 7, UserID: "other-reader", Role: types.TenantRoleAdmin, Status: types.TenantMemberStatusActive},
		{TenantID: 8, UserID: "reader", Role: types.TenantRoleOwner, Status: types.TenantMemberStatusActive},
	} {
		require.NoError(t, db.Create(&member).Error)
	}
	for i, row := range []types.WorkspaceNotification{
		{TenantID: 7, RecipientUserID: "reader", ActorUserID: "actor", Action: types.AuditActionMemberAdded},
		{TenantID: 7, RecipientUserID: "other-reader", ActorUserID: "actor", Action: types.AuditActionMemberRemoved},
		{TenantID: 8, RecipientUserID: "reader", ActorUserID: "foreign", Action: types.AuditActionMemberAdded},
		{TenantID: 7, RecipientUserID: "reader", ActorUserID: "actor", Action: types.AuditActionInvitationSent},
	} {
		row.AuditLogID = uint64(i + 1)
		row.CreatedAt = time.Now()
		require.NoError(t, db.Create(&row).Error)
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = types.TaskInitiator{UserID: "reader", Role: types.TenantRoleAdmin}.Apply(ctx)
	return db, NewWorkspaceNotificationService(db), ctx
}

func TestWorkspaceMemberManagementNotifiesOnlyAfterSuccessfulChangedOperation(t *testing.T) {
	db, _, ctx := workspaceNotificationFixture(t)
	require.NoError(t, db.AutoMigrate(&types.AuditLog{}))
	require.NoError(t, db.Where("1 = 1").Delete(&types.WorkspaceNotification{}).Error)
	require.NoError(t, db.Model(&types.TenantMember{}).Where("tenant_id = 7 AND user_id = ?", "reader").Update("role", "owner").Error)
	require.NoError(t, db.Create(&types.TenantMember{TenantID: 7, UserID: "remaining-admin", Role: types.TenantRoleAdmin, Status: types.TenantMemberStatusActive}).Error)
	ctx = types.TaskInitiator{UserID: "reader", Role: types.TenantRoleOwner}.Apply(ctx)
	svc := NewTenantMemberService(repository.NewTenantMemberRepository(db), NewAuditLogService(repository.NewAuditLogRepository(db)), nil, nil)
	require.ErrorIs(t, svc.UpdateRole(ctx, "other-reader", 7, "invalid"), ErrInvalidTenantRole)
	require.NoError(t, svc.UpdateRole(ctx, "other-reader", 7, types.TenantRoleViewer))
	require.NoError(t, svc.UpdateRole(ctx, "other-reader", 7, types.TenantRoleViewer), "same-role retry must be a no-op")
	require.ErrorIs(t, svc.UpdateRole(ctx, "missing", 7, types.TenantRoleViewer), ErrMembershipNotFound)
	var rows []types.WorkspaceNotification
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, "remaining-admin", rows[0].RecipientUserID)
	require.Equal(t, "reader", rows[0].ActorUserID)
	require.Equal(t, "other-reader", rows[0].TargetUserID)
	require.Equal(t, types.AuditActionMemberRoleChanged, rows[0].Action)
	var events []types.AuditLog
	require.NoError(t, db.Find(&events).Error)
	require.Len(t, events, 1)
}

func TestWorkspaceAdministratorSelfDepartureNotifiesRemainingManagers(t *testing.T) {
	for _, role := range []types.TenantRole{types.TenantRoleAdmin, types.TenantRoleOwner} {
		t.Run(string(role), func(t *testing.T) {
			db, _, ctx := workspaceNotificationFixture(t)
			require.NoError(t, db.AutoMigrate(&types.AuditLog{}))
			require.NoError(t, db.Where("1 = 1").Delete(&types.WorkspaceNotification{}).Error)
			require.NoError(t, db.Model(&types.TenantMember{}).Where("tenant_id = 7 AND user_id = ?", "reader").Update("role", role).Error)
			require.NoError(t, db.Create(&types.TenantMember{TenantID: 7, UserID: "remaining-owner", Role: types.TenantRoleOwner, Status: types.TenantMemberStatusActive}).Error)
			ctx = types.TaskInitiator{UserID: "reader", Role: role}.Apply(ctx)
			svc := NewTenantMemberService(repository.NewTenantMemberRepository(db), NewAuditLogService(repository.NewAuditLogRepository(db)), nil, nil)
			require.NoError(t, svc.RemoveMember(ctx, "reader", 7))
			var rows []types.WorkspaceNotification
			require.NoError(t, db.Order("recipient_user_id").Find(&rows).Error)
			require.Len(t, rows, 2)
			require.Equal(t, "other-reader", rows[0].RecipientUserID)
			require.Equal(t, "remaining-owner", rows[1].RecipientUserID)
			for _, row := range rows {
				require.Equal(t, types.AuditActionMemberLeft, row.Action)
				require.Equal(t, "reader", row.ActorUserID)
			}
		})
	}
}

func TestWorkspaceNotificationInboxScopesRecipientTenantAndCursor(t *testing.T) {
	_, svc, ctx := workspaceNotificationFixture(t)
	page, err := svc.List(ctx, 7, 0, 1)
	require.NoError(t, err)
	require.Len(t, page.Data, 1)
	require.Equal(t, uint64(4), page.Data[0].ID)
	require.Equal(t, "Actual operator", page.Data[0].ActorName)
	require.Equal(t, uint64(4), page.NextCursor)
	require.Equal(t, int64(2), page.UnreadCount)
	next, err := svc.List(ctx, 7, page.NextCursor, 1000)
	require.NoError(t, err)
	require.Len(t, next.Data, 1)
	require.Equal(t, uint64(1), next.Data[0].ID)
	require.Zero(t, next.NextCursor)
	require.Equal(t, int64(2), next.UnreadCount, "unread badge is independent of pagination")
}

func TestWorkspaceNotificationReadIsIdempotentAndCannotTouchAnotherInbox(t *testing.T) {
	_, svc, ctx := workspaceNotificationFixture(t)
	require.NoError(t, svc.MarkRead(ctx, 7, 4))
	page, err := svc.List(ctx, 7, 0, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), page.UnreadCount)
	readAt := page.Data[0].ReadAt
	require.NotNil(t, readAt)
	require.NoError(t, svc.MarkRead(ctx, 7, 4))
	reloaded, err := svc.List(ctx, 7, 0, 0)
	require.NoError(t, err)
	require.Equal(t, *readAt, *reloaded.Data[0].ReadAt)
	require.ErrorIs(t, svc.MarkRead(ctx, 7, 2), ErrWorkspaceNotificationNotFound)
	require.ErrorIs(t, svc.MarkRead(ctx, 7, 3), ErrWorkspaceNotificationNotFound)
	require.ErrorIs(t, svc.MarkRead(ctx, 7, 999), ErrWorkspaceNotificationNotFound)
}

func TestWorkspaceNotificationAccessRevokedAndCrossTenantDenied(t *testing.T) {
	db, svc, ctx := workspaceNotificationFixture(t)
	_, err := svc.List(ctx, 8, 0, 0)
	require.ErrorIs(t, err, ErrWorkspaceNotificationForbidden)
	require.ErrorIs(t, svc.MarkRead(ctx, 8, 3), ErrWorkspaceNotificationForbidden)
	require.NoError(t, db.Model(&types.TenantMember{}).Where("tenant_id = 7 AND user_id = ?", "reader").Update("role", "viewer").Error)
	_, err = svc.List(ctx, 7, 0, 0)
	require.NoError(t, err, "active members can still read notifications addressed to them before demotion")
	require.NoError(t, db.Model(&types.TenantMember{}).Where("tenant_id = 7 AND user_id = ?", "reader").Update("status", "suspended").Error)
	_, err = svc.List(ctx, 7, 0, 0)
	require.ErrorIs(t, err, ErrWorkspaceNotificationForbidden)
	require.ErrorIs(t, svc.MarkRead(ctx, 7, 4), ErrWorkspaceNotificationForbidden)
	require.NoError(t, db.Where("tenant_id = 7 AND user_id = ?", "reader").Delete(&types.TenantMember{}).Error)
	_, err = svc.List(ctx, 7, 0, 0)
	require.ErrorIs(t, err, ErrWorkspaceNotificationForbidden)
}
