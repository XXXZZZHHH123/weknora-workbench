package repository

import (
	"encoding/json"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func notifiesWorkspaceAdministrators(entry *types.AuditLog) bool {
	if entry == nil || entry.TenantID == 0 || entry.ActorUserID == "" ||
		(entry.ActorRole != string(types.TenantRoleAdmin) && entry.ActorRole != string(types.TenantRoleOwner)) ||
		entry.Outcome != types.AuditOutcomeSuccess || entry.ScopeType != "" {
		return false
	}
	switch entry.Action {
	case types.AuditActionMemberAdded, types.AuditActionMemberRemoved, types.AuditActionMemberRoleChanged, types.AuditActionMemberLeft,
		types.AuditActionInvitationSent, types.AuditActionInvitationRevoked:
		return true
	default:
		return false
	}
}

func notificationDetails(raw types.JSON) types.JSON {
	var source map[string]json.RawMessage
	_ = json.Unmarshal(raw, &source)
	details := make(map[string]json.RawMessage)
	for _, key := range []string{"old_role", "new_role", "role", "invitation_id"} {
		if value, ok := source[key]; ok {
			details[key] = value
		}
	}
	b, _ := json.Marshal(details)
	return types.JSON(b)
}

func createWorkspaceAdminNotifications(tx *gorm.DB, entry *types.AuditLog) error {
	// Context roles describe the operation before it happened. A successful
	// self-departure is the one case where that same operation removed the actor.
	var actorCount int64
	if err := tx.Model(&types.TenantMember{}).
		Where("tenant_id = ? AND user_id = ? AND status = ?", entry.TenantID, entry.ActorUserID, types.TenantMemberStatusActive).
		Count(&actorCount).Error; err != nil {
		return err
	}
	if actorCount == 0 {
		caller := types.CallerFromContext(tx.Statement.Context)
		if entry.Action != types.AuditActionMemberLeft || entry.TargetType != "tenant_member" ||
			entry.TargetUserID != entry.ActorUserID || caller.UserID != entry.ActorUserID ||
			caller.TenantID != entry.TenantID || string(caller.Role) != entry.ActorRole {
			return nil
		}
		if err := tx.Unscoped().Model(&types.TenantMember{}).
			Where("tenant_id = ? AND user_id = ? AND status = ? AND role = ? AND deleted_at IS NOT NULL",
				entry.TenantID, entry.ActorUserID, types.TenantMemberStatusActive, entry.ActorRole).
			Count(&actorCount).Error; err != nil {
			return err
		}
		if actorCount == 0 {
			return nil
		}
	}
	var recipients []string
	if err := tx.Model(&types.TenantMember{}).
		Where("tenant_id = ? AND status = ? AND role IN ? AND user_id <> ?", entry.TenantID, types.TenantMemberStatusActive,
			[]types.TenantRole{types.TenantRoleOwner, types.TenantRoleAdmin}, entry.ActorUserID).
		Distinct("user_id").Pluck("user_id", &recipients).Error; err != nil {
		return err
	}
	if len(recipients) == 0 {
		return nil
	}
	rows := make([]types.WorkspaceNotification, 0, len(recipients))
	for _, userID := range recipients {
		rows = append(rows, types.WorkspaceNotification{
			TenantID: entry.TenantID, RecipientUserID: userID, AuditLogID: entry.ID,
			ActorUserID: entry.ActorUserID, Action: entry.Action, TargetUserID: entry.TargetUserID,
			TargetID: entry.TargetID, Details: notificationDetails(entry.Details), CreatedAt: entry.CreatedAt,
		})
	}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "recipient_user_id"}, {Name: "audit_log_id"}}, DoNothing: true}).
		CreateInBatches(rows, 100).Error
}
