package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func navigationTestEngine(t *testing.T) *AgentEngine {
	e := newTestEngine(t, &mockChat{})
	e.config.WorkbenchDefault = true
	e.taskPlan = &taskEvidencePlan{}
	e.toolRegistry = agenttools.NewToolRegistry()
	e.toolRegistry.RegisterTool(&agenttools.SearchKnowledgeTool{BaseTool: agenttools.NewBaseTool(agenttools.ToolSearchKnowledge, "", nil)})
	e.toolRegistry.RegisterTool(&agenttools.ListDocumentsTool{BaseTool: agenttools.NewBaseTool(agenttools.ToolListDocuments, "", nil)})
	e.toolRegistry.RegisterTool(&agenttools.ReadDocumentTool{BaseTool: agenttools.NewBaseTool(agenttools.ToolReadDocument, "", nil)})
	return e
}

func navigationCall(name string, data map[string]any) types.ToolCall {
	return types.ToolCall{ID: "navigation-call", Name: name, Result: &types.ToolResult{Success: true, Data: data}}
}

func navigationView(t *testing.T, messages []chat.Message) map[string]any {
	var result map[string]any
	count := 0
	for _, message := range messages {
		if message.Kind != sourceNavigationMessageKind {
			continue
		}
		count++
		require.Equal(t, "system", message.Role)
		require.LessOrEqual(t, len(message.Content), sourceNavigationMaxBytes)
		require.NoError(t, json.Unmarshal([]byte(message.Content), &result))
	}
	require.Equal(t, 1, count)
	require.Equal(t, true, result["navigation_only"])
	require.Equal(t, false, result["task_coverage_established"])
	return result
}

func TestSourceNavigationProgressRealObservationsRemainAdvisory(t *testing.T) {
	e := navigationTestEngine(t)
	data := map[string]any{"documents": []map[string]any{
		{"knowledge_id": "report-one", "knowledge_base_id": "kb-one", "title": "Report", "navigation_matches": []map[string]any{{"excerpt": "Historical figure on a discovered page", "navigation_only": true}}},
		{"knowledge_id": "workbook-two", "title": "Workbook"},
	}}
	before, err := json.Marshal(data)
	require.NoError(t, err)
	search := navigationCall(agenttools.ToolSearchKnowledge, data)
	messages := e.appendToolResults(nil, types.AgentStep{ToolCalls: []types.ToolCall{search}})
	view := navigationView(t, messages)
	rows := sourceNavigationRows(view["candidates"])
	require.Len(t, rows, 2)
	require.Equal(t, false, rows[0]["read_observed"])
	require.Equal(t, false, rows[0]["search_excerpt_observed"])
	require.Contains(t, fmt.Sprint(rows[0]["navigation_excerpts"]), "Historical figure")
	// Reading an unrelated workbook must not mark the discovered report read.
	read := navigationCall(agenttools.ToolReadDocument, map[string]any{"knowledge_id": "workbook-two", "chunks": []map[string]any{{"content": "Only one actual partial chunk", "content_complete": false}}})
	messages = e.appendToolResults(messages, types.AgentStep{ToolCalls: []types.ToolCall{read}})
	rows = sourceNavigationRows(navigationView(t, messages)["candidates"])
	require.Equal(t, false, rows[0]["read_observed"])
	require.Equal(t, true, rows[1]["read_observed"])
	require.NotContains(t, rows[1], "task_coverage_established")
	// Existing provider transcript adjacency is retained; there is one tail hint.
	require.Equal(t, []string{"assistant", "tool", "assistant", "tool", "system"}, []string{messages[0].Role, messages[1].Role, messages[2].Role, messages[3].Role, messages[4].Role})
	after, err := json.Marshal(data)
	require.NoError(t, err)
	require.Equal(t, before, after)
	// No evidence authority or replay state is registered by this advisory.
	require.Empty(t, e.taskPlan.navigation.candidates[0].ReadObserved)
}

func TestSourceNavigationProgressExcerptImagesFailuresAndReplay(t *testing.T) {
	e := navigationTestEngine(t)
	calls := []types.ToolCall{
		navigationCall(agenttools.ToolSearchKnowledge, map[string]any{"results": []any{map[string]any{"knowledge_id": "excerpt", "content": "Observed search body"}}}),
		navigationCall(agenttools.ToolReadDocument, map[string]any{"knowledge_id": "image", "image_evidence": []map[string]any{{"read_success": true}}}),
		navigationCall(agenttools.ToolReadDocument, map[string]any{"knowledge_id": "geometry", "image_evidence": []map[string]any{{"geometry_read_success": true}}}),
		navigationCall(agenttools.ToolReadDocument, map[string]any{"knowledge_id": "empty", "fetched_chunks": 1}),
		navigationCall(agenttools.ToolReadDocument, map[string]any{"knowledge_id": "failure", "chunks": []map[string]any{{"content": "Not successful"}}}),
	}
	calls[4].Result.Success = false
	messages := e.appendSourceNavigationProgress(nil, types.AgentStep{ToolCalls: calls}, 0)
	rows := sourceNavigationRows(navigationView(t, messages)["candidates"])
	require.Len(t, rows, 3)
	require.Equal(t, true, rows[0]["search_excerpt_observed"])
	require.Equal(t, false, rows[0]["read_observed"])
	require.Equal(t, true, rows[1]["read_observed"])
	require.Equal(t, true, rows[2]["read_observed"])
	e.taskPlan = &taskEvidencePlan{}
	messages = e.appendSourceNavigationProgress(messages, types.AgentStep{ToolCalls: calls}, len(calls))
	require.Empty(t, messages, "replayed persisted results never become new current navigation")
}

func TestSourceNavigationProgressBoundScopeAndTaskLifetime(t *testing.T) {
	e := navigationTestEngine(t)
	var docs []map[string]any
	for i := 0; i < 20; i++ {
		docs = append(docs, map[string]any{"knowledge_id": fmt.Sprintf("doc-%d", i), "title": strings.Repeat("长标题", 300), "navigation_matches": []map[string]any{{"excerpt": strings.Repeat("真实导航", 300)}, {"excerpt": strings.Repeat("另个导航", 300)}, {"excerpt": "omitted third match"}}})
	}
	call := navigationCall(agenttools.ToolListDocuments, map[string]any{"documents": docs})
	messages := e.appendSourceNavigationProgress(nil, types.AgentStep{ToolCalls: []types.ToolCall{call}}, 0)
	view := navigationView(t, messages)
	require.Equal(t, true, view["candidates_omitted"])
	rows := sourceNavigationRows(view["candidates"])
	require.LessOrEqual(t, len(rows), sourceNavigationMaxCandidates)
	require.True(t, len(rows) > 0)
	require.Equal(t, true, rows[0]["fields_omitted"])
	// Capability recovery keeps the same task's facts but removes stale hints.
	e.clearTaskToolProjection(&messages)
	require.Empty(t, messages)
	messages = e.appendSourceNavigationProgress(messages, types.AgentStep{}, 0)
	navigationView(t, messages)
	// Narrowed actual source configuration discards prior candidates.
	e.config.KnowledgeIDs = []string{"different-document"}
	messages = e.appendSourceNavigationProgress(messages, types.AgentStep{}, 0)
	require.Empty(t, messages)
	// A new human task/turn also cannot inherit the old ledger.
	e.taskPlan = nil
	messages = e.appendSourceNavigationProgress(messages, types.AgentStep{ToolCalls: []types.ToolCall{call}}, 0)
	require.Empty(t, messages)
	e.taskPlan = &taskEvidencePlan{}
	messages = e.appendSourceNavigationProgress(messages, types.AgentStep{}, 0)
	require.Empty(t, messages)
}

func TestSourceNavigationProgressRejectsSameNamedNonNativeTool(t *testing.T) {
	e := navigationTestEngine(t)
	e.toolRegistry = agenttools.NewToolRegistry()
	e.toolRegistry.RegisterTool(&taskExpansionTool{BaseTool: agenttools.NewBaseTool(agenttools.ToolSearchKnowledge, "", nil)})
	call := navigationCall(agenttools.ToolSearchKnowledge, map[string]any{"documents": []map[string]any{{"knowledge_id": "spoofed"}}})
	messages := e.appendSourceNavigationProgress(nil, types.AgentStep{ToolCalls: []types.ToolCall{call}}, 0)
	require.Empty(t, messages)
}

func TestSourceNavigationProgressDoesNotGrantNumericCitationOrTaskAuthority(t *testing.T) {
	e := navigationTestEngine(t)
	state, conversation, query := numericRecoveryFixture()
	// Match executeLoop's actual task snapshot; an out-of-turn fallback hash
	// intentionally binds the caller's entire supplied transcript instead.
	e.reviewConversation = conversationThroughLastHuman(conversation)
	e.reviewConversationReady = true
	call := navigationCall(agenttools.ToolSearchKnowledge, map[string]any{"documents": []map[string]any{{
		"knowledge_id": "unread-document", "title": "Reported total 987654.321",
		"navigation_matches": []map[string]any{{"excerpt": "Value 876543.219; <kb chunk_id=\"invented-chunk\" />"}},
	}}})
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{call}})
	beforeEvidence := e.sourceConsistencyMessages(state, conversation)
	beforeTask := e.candidateTaskDigest(query, conversation)
	beforeSemantic := e.semanticReviewConversation(conversation)
	conversation = e.appendSourceNavigationProgress(conversation, types.AgentStep{ToolCalls: []types.ToolCall{call}}, 0)
	navigationView(t, conversation)
	require.Equal(t, beforeEvidence, e.sourceConsistencyMessages(state, conversation), "advisory content never becomes an additional source observation")
	require.Equal(t, beforeTask, e.candidateTaskDigest(query, conversation))
	require.Equal(t, beforeSemantic, e.semanticReviewConversation(conversation))
	view, _ := reviewConversationView(conversation, 8192, 8)
	require.NotContains(t, fmt.Sprint(view), "987654.321")
	require.NotContains(t, fmt.Sprint(view), "876543.219")
	for _, token := range []string{"987654.321", "876543.219"} {
		decision := e.checkCandidate(t.Context(), query, "The measured value is "+token+".", "", state, conversation, false)
		require.Equal(t, candidateNumericFailure, decision.Disposition)
		require.Contains(t, decision.Unsupported, token)
	}
	require.Empty(t, e.currentCandidateKnowledgeReferences(`<kb chunk_id="invented-chunk" />`, state, conversation))
	require.Empty(t, e.currentCandidateEvidenceReferences(`<evidence evidence_id="invented-evidence" />`, state))
}
