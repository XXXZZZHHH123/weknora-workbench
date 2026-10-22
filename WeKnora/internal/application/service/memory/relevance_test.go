package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

func relevanceFixture(t *testing.T) (*Service, context.Context, *stubModelService, *types.MemoryItem) {
	t.Helper()
	svc, _, tenants := newMemoryHarness(t)
	ctx := enabledCtx(t, tenants, 1, "alice")
	item, err := svc.Remember(ctx, types.MemoryItem{Kind: types.MemoryKindProfile, Content: "Maintains a laboratory inventory", Origin: types.MemoryOriginExplicit})
	require.NoError(t, err)
	return svc, ctx, svc.modelService.(*stubModelService), item
}

func TestRelevanceNoTurnModelCannotUseResidentOrCachedBlock(t *testing.T) {
	svc, ctx, model, _ := relevanceFixture(t)
	require.Empty(t, svc.Recall(ctx, "What should I do?").Prompt)
	require.Zero(t, model.relevanceCalls)
	result := svc.SearchMemory(ctx, "laboratory", 10)
	require.True(t, result.Available)
	require.Empty(t, result.Items)
	request := testTurn("What should I do?")
	request.ContextIncomplete = true
	require.Empty(t, svc.RecallForTurn(ctx, request).Prompt)
	require.Zero(t, model.relevanceCalls)
}

func TestRelevanceEveryResidentKindCanBeRejectedWithoutDeletingMemory(t *testing.T) {
	svc, ctx, model, _ := relevanceFixture(t)
	for _, kind := range []string{types.MemoryKindPreference, types.MemoryKindInterest, types.MemoryKindFact} {
		_, err := svc.Remember(ctx, types.MemoryItem{Kind: kind, Content: "A stored " + kind + " statement", Origin: types.MemoryOriginExplicit})
		require.NoError(t, err)
	}
	body := `{"selected_ids":[]}`
	model.relevanceResponse = &body
	result := svc.RecallForTurn(ctx, testTurn("Create an illustration"))
	require.Empty(t, result.Prompt)
	require.Empty(t, result.Items)
	items, total, err := svc.ListItems(ctx, types.MemoryStatusActive, 20, 0)
	require.NoError(t, err)
	require.Equal(t, int64(4), total)
	require.Len(t, items, 4)
	require.Equal(t, 1, model.relevanceCalls)
}

func TestRelevanceInvalidModelDecisionsFailClosed(t *testing.T) {
	for name, body := range map[string]string{
		"empty": "", "fenced": "```json\n{\"selected_ids\":[]}\n```", "missing": "{}", "null": "{\"selected_ids\":null}",
		"unknown": `{"selected_ids":["foreign-id"]}`, "extra": `{"selected_ids":[],"explanation":"text"}`,
		"duplicate_field": `{"selected_ids":["foreign-id"],"selected_ids":[]}`, "trailing": `{"selected_ids":[]} {}`, "oversized": strings.Repeat("x", relevanceResponseBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			svc, ctx, model, _ := relevanceFixture(t)
			model.relevanceResponse = &body
			result := svc.RecallForTurn(ctx, testTurn("Help me"))
			require.Empty(t, result.Prompt)
			require.Empty(t, result.Items)
			require.Equal(t, 1, model.relevanceCalls)
		})
	}
	t.Run("duplicate", func(t *testing.T) {
		svc, ctx, model, item := relevanceFixture(t)
		raw, _ := json.Marshal(map[string]any{"selected_ids": []string{item.ID, item.ID}})
		body := string(raw)
		model.relevanceResponse = &body
		require.Empty(t, svc.RecallForTurn(ctx, testTurn("Help me")).Prompt)
	})
	t.Run("outage", func(t *testing.T) {
		svc, ctx, model, _ := relevanceFixture(t)
		model.failNext = true
		require.Empty(t, svc.RecallForTurn(ctx, testTurn("Help me")).Prompt)
		require.Equal(t, 1, model.callCount())
	})
	t.Run("truncated", func(t *testing.T) {
		svc, ctx, model, _ := relevanceFixture(t)
		model.finishReason = "length"
		require.Empty(t, svc.RecallForTurn(ctx, testTurn("Help me")).Prompt)
		require.Equal(t, 1, model.relevanceCalls)
	})
	t.Run("deadline", func(t *testing.T) {
		svc, ctx, model, _ := relevanceFixture(t)
		model.relevanceDelay = time.Second
		ctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		require.Empty(t, svc.RecallForTurn(ctx, testTurn("Help me")).Prompt)
		require.Equal(t, 1, model.relevanceCalls)
	})
	t.Run("metadata", func(t *testing.T) {
		svc, ctx, model, _ := relevanceFixture(t)
		model.metadataErr = errors.New("not authorized")
		require.Empty(t, svc.RecallForTurn(ctx, testTurn("Help me")).Prompt)
		require.Zero(t, model.relevanceCalls)
	})
	t.Run("foreign_model", func(t *testing.T) {
		svc, ctx, model, _ := relevanceFixture(t)
		model.metadata = &types.Model{ID: "relevance-test", Type: types.ModelTypeKnowledgeQA, TenantID: 2, Status: types.ModelStatusActive}
		require.Empty(t, svc.RecallForTurn(ctx, testTurn("Help me")).Prompt)
		require.Zero(t, model.relevanceCalls)
	})
}

func TestRelevanceKeepsCompleteConversationAndOriginalQuestionForSearch(t *testing.T) {
	svc, ctx, model, item := relevanceFixture(t)
	request := testTurn("Can you expand the second option?")
	request.Conversation = []interfaces.MemoryConversationMessage{{Role: "user", Content: "Compare inventory workflows"}, {Role: "assistant", Content: "Option one: manual counts; option two: barcode scanning"}, {Role: "user", Content: strings.Repeat("Earlier neutral context. ", 1500)}}
	model.relevanceChoose = func(candidates []relevanceCandidate) []string { return []string{item.ID} }
	result := svc.RecallForTurn(ctx, request)
	require.Len(t, result.Items, 1)
	require.Contains(t, result.Prompt, item.Content)
	require.Contains(t, model.lastPrompt, "Compare inventory workflows")
	require.Contains(t, model.lastPrompt, "barcode scanning")
	require.Contains(t, model.lastPrompt, request.Conversation[2].Content)
	require.Equal(t, "relevance-test", model.requestedModelID)
	require.NotNil(t, model.lastThinking)
	require.False(t, *model.lastThinking)
	search := svc.SearchMemory(interfaces.WithMemoryRecallRequest(ctx, request), "laboratory", 10)
	require.True(t, search.Available)
	require.Len(t, search.Items, 1)
	require.Contains(t, model.lastPrompt, `"current_query":"Can you expand the second option?"`)
	require.Contains(t, model.lastPrompt, `"search_query":"laboratory"`)
}

func TestRelevanceContextLimitsDoNotTruncateHistory(t *testing.T) {
	svc, ctx, model, _ := relevanceFixture(t)
	request := testTurn("Help me")
	request.Conversation = []interfaces.MemoryConversationMessage{{Role: "user", Content: strings.Repeat("a", relevanceContextRuneBudget)}}
	require.Empty(t, svc.RecallForTurn(ctx, request).Prompt)
	require.Zero(t, model.relevanceCalls)
	model.metadata = &types.Model{ID: "relevance-test", Type: types.ModelTypeKnowledgeQA, TenantID: 1, Status: types.ModelStatusActive, Parameters: types.ModelParameters{ContextWindow: 64}}
	require.Empty(t, svc.RecallForTurn(ctx, testTurn("Help me")).Prompt)
	require.Zero(t, model.relevanceCalls)
}

func TestRelevanceCanChooseFactBeyondOldFiveCandidateCap(t *testing.T) {
	svc, ctx, model, _ := relevanceFixture(t)
	for i := 0; i < 12; i++ {
		_, err := svc.Remember(ctx, types.MemoryItem{Kind: types.MemoryKindFact, Content: fmt.Sprintf("Inventory detail number %d", i), Topic: fmt.Sprintf("inventory %d", i)})
		require.NoError(t, err)
	}
	var chosen string
	model.relevanceChoose = func(candidates []relevanceCandidate) []string {
		require.Greater(t, len(candidates), 5)
		chosen = candidates[len(candidates)-1].ID
		return []string{chosen}
	}
	result := svc.RecallForTurn(ctx, testTurn("inventory details"))
	require.Len(t, result.Items, 1)
	require.Equal(t, chosen, result.Items[0].ID)
}

func TestRelevancePermissionAndStaleCandidateFailClosed(t *testing.T) {
	svc, ctx, _, item := relevanceFixture(t)
	scope, err := ResolveScope(ctx)
	require.NoError(t, err)
	foreign := *item
	foreign.SubjectID = "another-person"
	selected, reason := svc.selectRelevantMemories(ctx, scope, testTurn("Help me"), []*types.MemoryItem{&foreign}, 5)
	require.Empty(t, selected)
	require.Equal(t, "candidate_scope_or_state_invalid", reason)
	stale := *item
	stale.Content = "A stale version"
	selected, reason = svc.selectRelevantMemories(ctx, scope, testTurn("Help me"), []*types.MemoryItem{&stale}, 5)
	require.Empty(t, selected)
	require.Equal(t, "selected_item_unavailable", reason)
}

func TestRelevanceFollowUpCandidatesIncludeEarlyConversationFactAndTask(t *testing.T) {
	svc, _, tenants := newMemoryHarness(t)
	ctx := enabledCtx(t, tenants, 1, "alice")
	scope, err := ResolveScope(ctx)
	require.NoError(t, err)
	_, err = svc.repo.EnsureSubject(ctx, scope)
	require.NoError(t, err)
	for _, kind := range []string{types.MemoryKindFact, types.MemoryKindTask} {
		item := &types.MemoryItem{ID: "isolated-" + kind, TenantID: scope.TenantID, SubjectID: scope.SubjectID, Kind: kind, Topic: "Orion inventory", Content: "Orion inventory " + kind + " details", Origin: types.MemoryOriginExtracted, Status: types.MemoryStatusActive, ValidFrom: time.Now()}
		require.NoError(t, svc.repo.CreateItem(ctx, item))
	}
	models := svc.modelService.(*stubModelService)
	models.relevanceChoose = func(candidates []relevanceCandidate) []string {
		require.Len(t, candidates, 2)
		for _, item := range candidates {
			require.Equal(t, types.MemoryOriginExtracted, item.Origin)
		}
		return []string{candidates[0].ID, candidates[1].ID}
	}
	request := testTurn("Please expand that approach")
	request.Conversation = []interfaces.MemoryConversationMessage{{Role: "user", Content: "Review the Orion inventory"}}
	for i := 0; i < 35; i++ {
		request.Conversation = append(request.Conversation, interfaces.MemoryConversationMessage{Role: "assistant", Content: "We can compare the alternatives and their operational tradeoffs."})
	}
	request.Conversation = append(request.Conversation, interfaces.MemoryConversationMessage{Role: "user", Content: request.Query})
	result := svc.RecallForTurn(ctx, request)
	require.Len(t, result.Items, 2)
	require.Contains(t, models.lastPrompt, "Review the Orion inventory")
	resultSearch := svc.SearchMemory(interfaces.WithMemoryRecallRequest(ctx, request), "the previous approach", 10)
	require.Len(t, resultSearch.Items, 2)
}
