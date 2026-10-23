package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestKnowledgeMetadataChangeAuditsActualEditorWithoutPrivateValues(t *testing.T) {
	audit := &captureKBActivityAudit{}
	repo := &metadataUpdateKnowledgeRepo{knowledge: &types.Knowledge{
		ID: "doc-1", TenantID: 7, KnowledgeBaseID: "kb-1", Title: "Before", Description: "old description",
	}}
	svc := &knowledgeService{repo: repo, kbService: metadataUpdateKBService{}, audit: audit}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = types.TaskInitiator{UserID: "actual-editor", Role: types.TenantRoleContributor}.Apply(ctx)
	ctx = (&access.KBAccess{KnowledgeBase: &types.KnowledgeBase{ID: "kb-1", TenantID: 7},
		Caller: types.CallerFromContext(ctx), EffectiveTenantID: 7, Permission: types.OrgRoleEditor}).Context(ctx)
	require.NoError(t, svc.UpdateKnowledge(ctx, &types.Knowledge{ID: "doc-1", Title: "After",
		Description: "private new description", DescriptionSpecified: true, CustomMetadata: types.JSON(`{"secret":"private-value"}`)}))
	require.NotNil(t, audit.entry, "metadata changes must leave an attributed activity event")
	require.Equal(t, "actual-editor", audit.entry.ActorUserID)
	require.Equal(t, "contributor", audit.entry.ActorRole)
	require.Equal(t, types.AuditActionKnowledgeUpdated, audit.entry.Action)
	require.Equal(t, "knowledge_base", audit.entry.ScopeType)
	require.Equal(t, "kb-1", audit.entry.ScopeID)
	require.Equal(t, "doc-1", audit.entry.TargetID)
	require.NotContains(t, string(audit.entry.Details), "private new description")
	require.NotContains(t, string(audit.entry.Details), "private-value")
}

func TestKnowledgeMetadataNoopAndDeniedWritesDoNotCreateChangeEvents(t *testing.T) {
	f := newDocumentWriteFixture(t)
	audit := &captureKBActivityAudit{}
	f.svc.audit = audit
	require.NoError(t, f.svc.UpdateKnowledge(f.ctx, &types.Knowledge{ID: "doc", Title: "original"}))
	require.Nil(t, audit.entry)
	denied := types.WithExecutionTenant(types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "viewer", Role: types.TenantRoleViewer}), 7)
	require.Error(t, f.svc.UpdateKnowledge(denied, &types.Knowledge{ID: "doc", Title: "not permitted"}))
	require.Nil(t, audit.entry)
}

type captureFileChanges struct {
	captureKBActivityAudit
	entries []*types.AuditLog
}

func (c *captureFileChanges) Log(_ context.Context, entry *types.AuditLog) error {
	c.entries = append(c.entries, entry)
	return nil
}

func TestKnowledgeDirectoryAndFileChangesAttributeActualAdministratorOrEditor(t *testing.T) {
	for _, role := range []types.TenantRole{types.TenantRoleAdmin, types.TenantRoleContributor} {
		t.Run(string(role), func(t *testing.T) {
			f := newDocumentWriteFixture(t)
			f.svc.repo = repository.NewKnowledgeRepository(f.db)
			audit := &captureFileChanges{}
			f.svc.audit = audit
			ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "actual-operator", Role: role})
			ctx = (&access.KBAccess{KnowledgeBase: f.kbs.values["kb"], Caller: types.CallerFromContext(ctx),
				EffectiveTenantID: 7, Permission: types.OrgRoleEditor}).Context(ctx)
			ctx = context.WithValue(ctx, types.TenantInfoContextKey, &types.Tenant{ID: 7})
			_, err := f.svc.CreateKnowledgeFolder(ctx, "kb", "Folder")
			require.NoError(t, err)
			_, err = f.svc.CreateKnowledgeFolder(ctx, "kb", "Folder")
			require.Error(t, err)
			require.Len(t, audit.entries, 1, "a rejected duplicate folder is not a successful change")
			_, err = f.svc.MoveKnowledgeToFolder(ctx, "kb", []string{"doc"}, "Folder")
			require.NoError(t, err)
			require.Error(t, f.svc.DeleteKnowledgeFolder(ctx, "kb", "Folder"))
			require.Len(t, audit.entries, 2, "a rejected nonempty deletion must not emit a deletion event")
			_, err = f.svc.RenameKnowledgeFolder(ctx, "kb", "Folder", "Renamed")
			require.NoError(t, err)
			require.NoError(t, f.svc.UpdateKnowledge(ctx, &types.Knowledge{ID: "doc", Title: "Updated"}))
			require.NoError(t, f.svc.DeleteKnowledgeFolder(ctx, "kb", "Renamed", true))
			var actions []types.AuditAction
			for _, event := range audit.entries {
				require.Equal(t, "actual-operator", event.ActorUserID)
				require.Equal(t, string(role), event.ActorRole)
				require.Equal(t, uint64(7), event.TenantID)
				require.Equal(t, "kb", event.ScopeID)
				actions = append(actions, event.Action)
			}
			for _, action := range []types.AuditAction{types.AuditActionFolderCreated, types.AuditActionKnowledgeFolderChanged,
				types.AuditActionFolderUpdated, types.AuditActionKnowledgeUpdated, types.AuditActionKnowledgeBatchDeleted, types.AuditActionFolderDeleted} {
				require.Contains(t, actions, action)
			}
			var detail map[string]any
			require.NoError(t, json.Unmarshal(audit.entries[1].Details, &detail))
			require.Equal(t, "Folder", detail["folder_path"])
		})
	}
}

func TestKnowledgeTagChangesAuditRealActorAndRejectUnauthorizedOrInvalidUpdates(t *testing.T) {
	for _, role := range []types.TenantRole{types.TenantRoleAdmin, types.TenantRoleContributor} {
		t.Run(string(role), func(t *testing.T) {
			f := newDocumentWriteFixture(t)
			audit := &captureFileChanges{}
			f.svc.audit = audit
			ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "tag-operator", Role: role})
			ctx = (&access.KBAccess{KnowledgeBase: f.kbs.values["kb"], Caller: types.CallerFromContext(ctx),
				EffectiveTenantID: 7, Permission: types.OrgRoleEditor}).Context(ctx)
			require.NoError(t, f.svc.SetKnowledgeTags(ctx, "doc", []string{"tag"}))
			require.NoError(t, f.svc.UpdateKnowledgeTag(ctx, "doc", nil))
			require.NoError(t, f.svc.UpdateKnowledgeTagBatch(ctx, "kb", map[string][]string{"doc": {"tag"}}))
			require.Len(t, audit.entries, 3)
			for _, entry := range audit.entries {
				require.Equal(t, "tag-operator", entry.ActorUserID)
				require.Equal(t, string(role), entry.ActorRole)
				require.Equal(t, types.AuditActionKnowledgeUpdated, entry.Action)
				require.Equal(t, "kb", entry.ScopeID)
				require.Equal(t, "doc", entry.TargetID)
				require.JSONEq(t, `{"fields":["tag_ids"]}`, string(entry.Details))
			}
			require.Error(t, f.svc.UpdateKnowledgeTag(ctx, "doc", []string{"missing-tag"}))
			require.Error(t, f.svc.UpdateKnowledgeTagBatch(ctx, "kb", map[string][]string{"other-doc": nil}))
			denied := types.WithExecutionTenant(types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "viewer", Role: types.TenantRoleViewer}), 7)
			requireForbiddenWrite(t, f.svc.UpdateKnowledgeTag(denied, "doc", []string{"tag"}))
			require.Len(t, audit.entries, 3)
			actual, err := f.svc.GetKnowledgeTags(ctx, []string{"doc"})
			require.NoError(t, err)
			require.Len(t, actual["doc"], 1)
			require.Equal(t, "tag", actual["doc"][0].ID)
		})
	}
}

func TestFileChangeAuditUsesAuthenticatedCallerAcrossExecutionContexts(t *testing.T) {
	ctx := types.TaskInitiator{UserID: "execution-user", Role: types.TenantRoleOwner}.Apply(context.Background())
	ctx = types.WithCaller(ctx, types.Caller{TenantID: 1, UserID: "real-editor", Role: types.TenantRoleContributor})
	ctx = types.WithExecutionTenant(ctx, 7)
	audit := &captureKBActivityAudit{}
	recordKBActivity(ctx, audit, 7, "kb", types.AuditActionKnowledgeUpdated, "knowledge", "doc", types.AuditOutcomeSuccess,
		map[string]any{"fields": []string{"tag_ids"}})
	require.NotNil(t, audit.entry)
	require.Equal(t, "real-editor", audit.entry.ActorUserID)
	require.Equal(t, "contributor", audit.entry.ActorRole)
	require.Equal(t, uint64(7), audit.entry.TenantID)
}
