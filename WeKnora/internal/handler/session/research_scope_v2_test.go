package session

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type researchV2KBs struct {
	interfaces.KnowledgeBaseService
	rows  map[string]*types.KnowledgeBase
	reads []string
}

func (f *researchV2KBs) GetKnowledgeBaseByIDOnly(_ context.Context, id string) (*types.KnowledgeBase, error) {
	f.reads = append(f.reads, id)
	return f.rows[id], nil
}

func TestResearchQuestionV2UsesDirectSourceListAndNeverBorrowDelegatedSource(t *testing.T) {
	h, workbenches, ctx, session := researchScopeFixture()
	workbenches.row.ScopeVersion = 2
	workbenches.row.SourcesRevision = 4
	workbenches.row.SourcesSummary = []types.WorkbenchSourceSummary{
		{ID: "first", SourceType: "knowledge_base", SourceID: "library-a", KnowledgeBaseID: "library-a", TenantID: 13, Status: "active", CanRead: true},
		{ID: "center", SourceType: "central_dataset", SourceID: "central", KnowledgeBaseID: "central-backing", TenantID: 7, Status: "active", CanRead: true},
		{ID: "delegated", SourceType: "knowledge_base", SourceID: "restricted", KnowledgeBaseID: "restricted", TenantID: 7, Status: "active", Delegated: true, ViewModules: []string{"diagram"}},
	}
	kbs := &researchV2KBs{rows: map[string]*types.KnowledgeBase{"library-a": {ID: "library-a", TenantID: 13}, "central-backing": {ID: "central-backing", TenantID: 7}}}
	h.knowledgebaseService = kbs
	request := &CreateKnowledgeQARequest{}
	scoped, err := h.bindResearchQuestionScope(ctx, session, request)
	require.NoError(t, err)
	require.Equal(t, []string{"library-a", "central-backing"}, request.KnowledgeBaseIDs)
	require.NotContains(t, kbs.reads, "restricted")
	grant, _ := types.ResearchWorkbenchScopeFromContext(scoped)
	require.EqualValues(t, 4, grant.SourcesRevision)
	_, err = h.bindResearchQuestionScope(ctx, session, &CreateKnowledgeQARequest{KnowledgeBaseIDs: []string{"restricted"}})
	require.Error(t, err)
}

func TestResearchQuestionV2EmptySourcesDoNotFallBackToLegacyKB(t *testing.T) {
	h, workbenches, ctx, session := researchScopeFixture()
	workbenches.row.ScopeVersion = 2
	workbenches.row.SourcesRevision = 2
	workbenches.row.KnowledgeBaseID = ""
	kbs := &researchV2KBs{rows: map[string]*types.KnowledgeBase{}}
	h.knowledgebaseService = kbs
	request := &CreateKnowledgeQARequest{}
	scoped, err := h.bindResearchQuestionScope(ctx, session, request)
	require.NoError(t, err)
	require.Empty(t, request.KnowledgeBaseIDs)
	require.Empty(t, kbs.reads)
	grant, _ := types.ResearchWorkbenchScopeFromContext(scoped)
	require.Equal(t, session.ResearchWorkbenchID, grant.ID)
	require.Empty(t, grant.KnowledgeBaseID)
}
