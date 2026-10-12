package service

import (
	"context"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestWorkbenchExplicitKnowledgeAndDocumentSelectionOverridesAllDefaults(t *testing.T) {
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "real-user", Role: types.TenantRoleViewer})
	svc := &sessionService{}
	for _, selection := range []struct{ kb, doc []string }{{[]string{"selected-kb"}, nil}, {nil, []string{"selected-document"}}, {[]string{"selected-kb"}, []string{"selected-document"}}} {
		kb, doc, err := svc.resolveKnowledgeBases(ctx, &types.QARequest{Session: &types.Session{TenantID: 7}, CustomAgent: &types.CustomAgent{WorkbenchDefault: true, TenantID: 7, Config: types.CustomAgentConfig{KBSelectionMode: "all", KnowledgeBases: []string{"other-kb"}}}, KnowledgeBaseIDs: selection.kb, KnowledgeIDs: selection.doc})
		require.NoError(t, err)
		require.Equal(t, selection.kb, kb)
		require.Equal(t, selection.doc, doc)
	}
}
func TestWorkbenchExplicitSelectionStillEnforcesRestrictedCallerScope(t *testing.T) {
	ctx := types.WithTenantAPIKeyScope(context.Background(), types.TenantAPIKeyScope{KnowledgeBaseIDs: types.StringArray{"allowed"}})
	svc := &sessionService{}
	_, _, err := svc.resolveKnowledgeBases(ctx, &types.QARequest{Session: &types.Session{TenantID: 7}, CustomAgent: &types.CustomAgent{WorkbenchDefault: true, TenantID: 7, Config: types.CustomAgentConfig{KBSelectionMode: "all"}}, KnowledgeBaseIDs: []string{"foreign"}})
	require.Error(t, err)
}
