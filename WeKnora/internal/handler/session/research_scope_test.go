package session

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type researchScopeWorkbenches struct {
	row   *types.ResearchWorkbench
	err   error
	calls int
}

func (f *researchScopeWorkbenches) GetWorkbench(context.Context, string) (*types.ResearchWorkbench, error) {
	f.calls++
	return f.row, f.err
}

type researchScopeKBs struct {
	interfaces.KnowledgeBaseService
}

func (*researchScopeKBs) GetKnowledgeBaseByIDOnly(context.Context, string) (*types.KnowledgeBase, error) {
	return &types.KnowledgeBase{ID: "library-a", TenantID: 13}, nil
}

type researchScopeFiles struct{ interfaces.KnowledgeService }

func (*researchScopeFiles) GetKnowledgeByIDOnly(context.Context, string) (*types.Knowledge, error) {
	return &types.Knowledge{ID: "foreign-file", KnowledgeBaseID: "library-b"}, nil
}

func researchScopeFixture() (*Handler, *researchScopeWorkbenches, context.Context, *types.Session) {
	row := &types.ResearchWorkbench{ID: "67175bd2-32c2-42df-8ba6-d8c2e2f1fe67", TenantID: 7, KnowledgeBaseID: "library-a", WorkspaceMode: "isolated"}
	workbenches := &researchScopeWorkbenches{row: row}
	h := &Handler{researchWorkbenches: workbenches, knowledgebaseService: &researchScopeKBs{}, knowledgeService: &researchScopeFiles{}}
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "user-a", Role: types.TenantRoleViewer})
	return h, workbenches, ctx, &types.Session{TenantID: 7, ResearchWorkbenchID: row.ID}
}

func TestBoundResearchSessionRechecksAccessAndPinsSourcesEveryTurn(t *testing.T) {
	h, workbenches, ctx, session := researchScopeFixture()
	for range 2 {
		request := &CreateKnowledgeQARequest{}
		scoped, err := h.bindResearchQuestionScope(ctx, session, request)
		require.NoError(t, err)
		require.Equal(t, []string{"library-a"}, request.KnowledgeBaseIDs)
		value, ok := types.ResearchWorkbenchScopeFromContext(scoped)
		require.True(t, ok)
		require.Equal(t, uint64(13), value.KnowledgeBaseTenantID)
	}
	require.Equal(t, 2, workbenches.calls)
	workbenches.err = errors.New("grant revoked")
	_, err := h.bindResearchQuestionScope(ctx, session, &CreateKnowledgeQARequest{})
	require.Error(t, err)
}

func TestLegacyWorkbenchSessionAlsoBindsKnowledgeBase(t *testing.T) {
	h, workbenches, ctx, session := researchScopeFixture()
	workbenches.row.WorkspaceMode = "legacy"
	request := &CreateKnowledgeQARequest{}
	scoped, err := h.bindResearchQuestionScope(ctx, session, request)
	require.NoError(t, err)
	require.Equal(t, []string{"library-a"}, request.KnowledgeBaseIDs)
	value, ok := types.ResearchWorkbenchScopeFromContext(scoped)
	require.True(t, ok)
	require.Equal(t, session.ResearchWorkbenchID, value.ID)
}

func TestBoundResearchSessionRejectsScopeWidening(t *testing.T) {
	h, workbenches, ctx, session := researchScopeFixture()
	for _, request := range []*CreateKnowledgeQARequest{
		{KnowledgeBaseIDs: []string{"library-b"}}, {KnowledgeIDs: []string{"foreign-file"}},
		{AgentID: "custom-other"}, {AgentSourceTenantID: 8}, {WebSearchEnabled: true},
		{MCPServiceIDs: []string{"remote"}}, {SkillNames: []string{"other"}},
		{MentionedItems: []MentionedItemRequest{{Type: "file", ID: "foreign-file", KBID: "library-b"}}},
	} {
		_, err := h.bindResearchQuestionScope(ctx, session, request)
		require.Error(t, err)
	}
	workbenches.row.WorkspaceMode = "unknown"
	_, err := h.resolveResearchWorkbenchScope(ctx, session)
	require.Error(t, err)
	workbenches.row.WorkspaceMode = "isolated"
	workbenches.row.TenantID = 8
	_, err = h.resolveResearchWorkbenchScope(ctx, session)
	require.Error(t, err)
}

func TestIsolatedResearchAgentDoesNotInheritGlobalMemoryOrSkills(t *testing.T) {
	t.Setenv("COMMODITY_RESEARCH_URL", "http://127.0.0.1:1")
	_, _, _, session := researchScopeFixture()
	enabled := true
	original := &types.CustomAgent{ID: types.BuiltinQuickAnswerID, IsBuiltin: true, Config: types.CustomAgentConfig{MemoryEnabled: &enabled, SkillsSelectionMode: "all", MCPSelectionMode: "all"}}
	agent := isolatedResearchAgent(original, types.ResearchWorkbenchScope{ID: session.ResearchWorkbenchID, TenantID: 7, KnowledgeBaseID: "library-a"})
	require.NotNil(t, agent)
	require.False(t, *agent.Config.MemoryEnabled)
	require.Equal(t, "none", agent.Config.SkillsSelectionMode)
	require.Equal(t, "none", agent.Config.MCPSelectionMode)
	require.Equal(t, []string{"library-a"}, agent.Config.KnowledgeBases)
	require.True(t, *original.Config.MemoryEnabled)
}

func TestIsolatedResearchAgentStaysRestrictedWithoutExtensionServices(t *testing.T) {
	t.Setenv("COMMODITY_RESEARCH_URL", "")
	t.Setenv("STRUCTURED_DATA_URL", "")
	agent := isolatedResearchAgent(nil, types.ResearchWorkbenchScope{TenantID: 7, KnowledgeBaseID: "library-a"})
	require.NotNil(t, agent)
	require.True(t, agent.WorkbenchDefault)
	require.False(t, *agent.Config.MemoryEnabled)
	require.Equal(t, "none", agent.Config.SkillsSelectionMode)
	require.Len(t, agent.Config.AllowedTools, 3)
}

type isolatedCompletionMessages struct {
	interfaces.MessageService
	updated int
}

func (s *isolatedCompletionMessages) UpdateMessage(_ context.Context, message *types.Message) error {
	s.updated++
	return nil
}

type isolatedCompletionTenants struct{ interfaces.TenantService }

func (*isolatedCompletionTenants) GetTenantByID(context.Context, uint64) (*types.Tenant, error) {
	panic("isolated research must not enter tenant-wide history indexing")
}

func TestIsolatedCompletionPersistsTranscriptWithoutPublishingGlobalHistory(t *testing.T) {
	messages := &isolatedCompletionMessages{}
	h := &Handler{messageService: messages, tenantService: &isolatedCompletionTenants{}}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = types.WithResearchWorkbenchScope(ctx, types.ResearchWorkbenchScope{ID: "67175bd2-32c2-42df-8ba6-d8c2e2f1fe67", TenantID: 7})
	message := &types.Message{ID: "answer", SessionID: "session", Content: "Private research"}
	require.NoError(t, h.completeAssistantMessage(ctx, message, "Question", "user-message"))
	require.Equal(t, 1, messages.updated)
	require.True(t, message.IsCompleted)
}

func TestResearchScopeConstructorNilServiceFailsClosed(t *testing.T) {
	h := NewHandler(
		nil, nil, nil, nil, nil, &researchScopeKBs{},
		nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil,
	)
	if h.researchWorkbenches != nil {
		t.Fatal("nil concrete dependency must not become a non-nil interface")
	}
	_, _, ctx, session := researchScopeFixture()
	require.NotPanics(t, func() {
		_, err := h.resolveResearchWorkbenchScope(ctx, session)
		require.Error(t, err)
	})
}
