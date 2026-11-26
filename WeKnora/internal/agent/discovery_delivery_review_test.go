package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func discoveryReviewFixture(t *testing.T, replies ...mockResponse) (*AgentEngine, *types.AgentState, *mockChat) {
	t.Helper()
	model := &mockChat{responses: replies}
	e := newTestEngine(t, model)
	e.config.AnswerReviewMode, e.config.WorkbenchDefault = "standard", true
	e.toolRegistry = agenttools.NewToolRegistry()
	for _, name := range []string{agenttools.ToolSearchKnowledge, agenttools.ToolReadDocument, agenttools.ToolBindDocumentScope, expandTaskToolsName} {
		e.toolRegistry.RegisterTool(newCountingTool(name))
	}
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{
		ID: "catalog-call", Name: "structured_list_series", Args: map[string]interface{}{"knowledge_base_id": "permitted", "search": "source measure"},
		Result: &types.ToolResult{Success: true, Output: "DISPLAY_ONLY_NOT_EVIDENCE", Data: map[string]any{
			"_structured_execution": agenttools.StructuredOperationDescriptor{Operation: "list_series", ToolName: "structured_list_series", EffectClass: "read", ScopeMode: "knowledge_base"},
			"discovery_coverage":    map[string]any{"operation": "list_series", "semantic_absence_established": false, "document_body_searched_by_this_operation": false},
			"pending_sheets":        []any{map[string]any{"name": "Combined measure", "status": "needs_mapping"}},
			"total":                 0, "next_offset": nil,
		}},
	}}}}}
	return e, state, model
}

func discoveryReply(action, feedback string) mockResponse {
	raw, _ := json.Marshal(map[string]string{"action": action, "feedback": feedback})
	return deliveryReply(string(raw), "stop", types.TokenUsage{PromptTokens: 40, CompletionTokens: 12, TotalTokens: 52})
}

func TestDiscoveryDeliveryReviewRecoversWithoutSelectingSourcesOrChangingEvidence(t *testing.T) {
	e, state, model := discoveryReviewFixture(t, discoveryReply("recover", "The requested trend remains unanswered. Discover an authorized report or original figure with the same metric definition."))
	before := reviewDigest(state.RoundSteps)
	const candidate = "The field mapping is pending, so first import the worksheet."
	decision := e.checkCandidate(t.Context(), "Explain the trend shown in the available sources.", candidate, "", state, nil, false)
	require.Equal(t, candidateNeedSource, decision.Disposition)
	require.Equal(t, "discovery_coverage_recovery", decision.ReasonCode)
	require.Equal(t, candidate, decision.Candidate)
	require.Equal(t, reviewCollectEvidence, e.answerReviewAction)
	require.Contains(t, e.answerReviewFeedback, "requested trend")
	require.Equal(t, before, reviewDigest(state.RoundSteps))
	require.Zero(t, e.turnReview.Repairs, "the ordinary executor owns the existing shared repair budget")
	require.False(t, decision.SourceReviewAttempted, "this is not the strict source critic")
	require.Len(t, model.calls, 1)
	require.Equal(t, "none", model.opts[0].ToolChoice)
	require.Empty(t, model.opts[0].Tools)
	require.Contains(t, model.calls[0][1].Content, "needs_mapping")
	require.Contains(t, model.calls[0][1].Content, "search_knowledge")
	require.NotContains(t, model.calls[0][1].Content, "DISPLAY_ONLY_NOT_EVIDENCE")
	require.Equal(t, 12, state.TurnUsage.CompletionTokens)
	for _, repairs := range []int{0, maxTurnCandidateRepairs} {
		e.turnReview.Repairs = repairs
		repeated := e.checkCandidate(t.Context(), "Explain the trend shown in the available sources.", candidate, "", state, nil, false)
		require.Equal(t, candidateNeedSource, repeated.Disposition, "unchanged candidate/evidence never becomes accepted when the reviewer budget is spent")
		require.Contains(t, e.answerReviewFeedback, "requested trend")
		require.Len(t, model.calls, 1)
	}
	e.turnReview.Repairs = 0
	// A second candidate gets no fresh reviewer call or independent repair loop.
	e.checkCandidate(t.Context(), "Explain the trend shown in the available sources.", "The available directory remains unmapped.", "", state, nil, false)
	require.Len(t, model.calls, 1)
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{{Name: agenttools.ToolReadDocument, Result: &types.ToolResult{Success: true, Output: "New actual authorized source text."}}}})
	newEvidence := e.checkCandidate(t.Context(), "Explain the trend shown in the available sources.", candidate, "", state, nil, false)
	require.NotEqual(t, "discovery_coverage_recovery", newEvidence.ReasonCode, "an old recovery decision cannot bind new evidence")
	require.Len(t, model.calls, 1)
}

func TestDiscoveryDeliveryReviewKeepsSatisfiedDirectoryAndRestrictedTasks(t *testing.T) {
	for _, query := range []string{
		"Which worksheet still needs field mapping?",
		"Use only published structured fields in this selected file; report unavailable fields without searching elsewhere.",
		"Explain what an unmapped worksheet means in general.",
	} {
		t.Run(query, func(t *testing.T) {
			e, state, model := discoveryReviewFixture(t, discoveryReply("keep", ""))
			e.selectedDocs = []*SelectedDocumentInfo{{KnowledgeID: "only-file", Title: "Only selected workbook", FileType: "xlsx"}}
			const candidate = "The combined measure worksheet needs field mapping."
			decision := e.checkCandidate(t.Context(), query, candidate, "", state, nil, false)
			require.Equal(t, candidateAccepted, decision.Disposition)
			require.Equal(t, candidate, decision.Candidate)
			require.Empty(t, e.answerReviewFeedback)
			require.Len(t, model.calls, 1)
			require.Contains(t, model.calls[0][1].Content, query)
			require.Contains(t, model.calls[0][1].Content, "Only selected workbook")
			require.Contains(t, model.calls[0][0].Content, "Respect explicit source restrictions")
		})
	}
}

func TestDiscoveryDeliveryReviewMissingPrimaryValueUsesOriginalScope(t *testing.T) {
	const question = "数据库里今天的读数是多少？"
	lastDate := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	candidate := "数据库今天无记录。最后有值的日期为 " + lastDate + "，库内原始读数为 0。"
	const fallback = "Return the last available database value when the requested reading is missing."
	const lead = "同期报告记载每日读数。"
	missing, _ := structuredQueryFixture(t, "structured_analyze_table", "requested", map[string]any{"sql": "SELECT value FROM data WHERE date = CURRENT_DATE AND metric = '读数'"}, map[string]any{
		"_structured_execution": agenttools.StructuredOperationDescriptor{Operation: "analyze_table", EffectClass: "read"},
		"status":                "ok", "rows": []any{}, "returned_row_count": 0, "input_complete": true, "output_truncated": false,
	}, true)
	latest, _ := structuredQueryFixture(t, "structured_query_series", "latest", nil, map[string]any{
		"_structured_execution": agenttools.StructuredOperationDescriptor{Operation: "query_series", EffectClass: "read"},
		"status":                "ok", "rows": []any{map[string]any{"date": lastDate, "value": 0}},
	}, true)
	for _, tc := range []struct{ name, prior, action, feedback string }{
		{"named_primary", "", "recover", "Look up the requested period and measure in the authorized reports and read the matching passage."},
		{"database_only", "只用数据库。", "keep", ""},
		{"table_only", "请只查当前表。", "keep", ""},
		{"workbook_only", "答复须限于这份工作簿。", "keep", ""},
		{"database_coverage", "在数据库能查到的范围内回答。", "keep", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, state, model := discoveryReviewFixture(t, discoveryReply(tc.action, tc.feedback))
			e.taskPlan = &taskEvidencePlan{
				SourceDiscovery:    &taskSourceDiscovery{Mode: "discover", SourceDomains: []string{"structured"}},
				CompletionCriteria: []string{fallback},
			}
			e.knowledgeBasesInfo = []*KnowledgeBaseInfo{{ID: "permitted", Description: lead}}
			state.RoundSteps = []types.AgentStep{{ToolCalls: []types.ToolCall{missing}}, {ToolCalls: []types.ToolCall{latest}}}
			var history []chat.Message
			if tc.prior != "" {
				history = []chat.Message{{Role: "user", Content: tc.prior}}
			}
			decision := e.checkCandidate(t.Context(), question, candidate, "", state, history, false)
			require.Len(t, model.calls, 1)
			input := model.calls[0][1].Content
			for _, evidence := range []string{question, candidate, fallback, lead, `"source_domains":["structured"]`, `"returned_row_count":0`, `"input_complete":true`} {
				require.Contains(t, input, evidence)
			}
			// The verdict is mocked. Check the policy actually sent with this empty SQL result and older observation.
			prompt := model.calls[0][0].Content
			require.Contains(t, prompt, "successful SQL query returning no matching rows")
			require.Contains(t, prompt, "independently of the advisory plan's latest-value fallback")
			if tc.action == "recover" {
				require.Equal(t, candidateNeedSource, decision.Disposition)
				require.Contains(t, e.answerReviewFeedback, tc.feedback)
				require.Contains(t, prompt, "supplementation is permitted and the authorized directory or navigation points to a relevant document or series")
				require.Contains(t, prompt, "search once for the requested period and measure and read the supporting passage or figure on a hit")
				require.Contains(t, prompt, "Read an existing hit directly and reuse evidence already read")
				require.Contains(t, prompt, "each source's date, measure, reported unit, original value and location separately")
				require.Contains(t, prompt, "without repeating the search or asking for material")
			} else {
				require.Equal(t, candidateAccepted, decision.Disposition)
				require.Contains(t, input, tc.prior)
				require.Contains(t, prompt, "to what the database can establish, stay within that source")
				require.Contains(t, prompt, "date, original value and provenance of the last valid observation")
				require.Contains(t, prompt, "without seeking supplementary documents or asking the user for material")
			}
		})
	}
}

func TestDiscoveryProgressReviewSeesNoDataWithinDatabaseScope(t *testing.T) {
	e, state, model, messages, guards := sourceProgressFixture(t, "keep")
	e.toolRegistry.RegisterTool(agenttools.NewStructuredDataTool())
	e.knowledgeBasesInfo = []*KnowledgeBaseInfo{{ID: "permitted", Description: "同期报告记载每日读数。"}}
	const question = "在数据库能查到的范围内给出今天的读数。"
	messages[1].Content = question
	e.reviewConversation = conversationThroughLastHuman(messages)
	day := time.Now().UTC().Format(time.DateOnly)
	args := map[string]any{"operation": "query_series", "arguments": map[string]any{"series_id": "reading", "start_date": day, "end_date": day}}
	missing, _ := structuredQueryFixture(t, agenttools.ToolStructuredData, "requested", args, map[string]any{
		"_structured_execution": agenttools.StructuredOperationDescriptor{Operation: "query_series", EffectClass: "read"},
		"status":                "no_data", "rows": []any{},
	}, true)
	state.RoundSteps[len(state.RoundSteps)-1].ToolCalls = []types.ToolCall{missing}
	e.reviewSourceProgress(t.Context(), question, state, &messages, guards, "session")
	require.Len(t, model.calls, 1)
	for _, fact := range []string{`"phase":"progress"`, `"status":"no_data"`, question, "同期报告记载每日读数。"} {
		require.Contains(t, model.calls[0][1].Content, fact)
	}
	require.Contains(t, model.calls[0][0].Content, "to what the database can establish, stay within that source")
	require.Zero(t, guards.evidenceRecoveryAttempts)
}

func TestDiscoveryDeliveryReviewClarificationAllowsModelLedParallelBindings(t *testing.T) {
	for _, action := range []string{"recover", "keep"} {
		t.Run(action, func(t *testing.T) {
			query := "Give the reported measure for that date."
			feedback := "Separate labeled source alternatives can answer the question; propose explicit parallel bindings for the source planner to validate."
			if action == "keep" {
				query = "Give only the one specific series I mean; ask me if its identity cannot be determined."
				feedback = ""
			}
			e, state, model := discoveryReviewFixture(t, discoveryReply(action, feedback))
			state.RoundSteps[0].ToolCalls = append(state.RoundSteps[0].ToolCalls,
				types.ToolCall{Name: agenttools.ToolSearchKnowledge, Result: &types.ToolResult{Success: true, Output: "Retrieved a document preview."}},
				types.ToolCall{Name: "structured_analyze_table", Result: &types.ToolResult{Success: true, Data: map[string]any{
					"_structured_execution": agenttools.StructuredOperationDescriptor{Operation: "analyze_table", ToolName: "structured_analyze_table", EffectClass: "read", ScopeMode: "knowledge_base"},
					"status":                "needs_clarification", "kind": "structured_source_binding",
					"fields":   []any{[]any{map[string]any{"series_id": "actual-a", "metric": "measure", "dimensions": map[string]any{"group": "A"}}, map[string]any{"series_id": "actual-b", "metric": "measure", "dimensions": map[string]any{"group": "B"}}}},
					"bindings": []any{map[string]any{"selected_ids": []string{}, "considered_ids": []string{"actual-a", "actual-b"}, "reason": "Multiple real source alternatives"}},
				}}},
			)
			before := reviewDigest(state.RoundSteps)
			candidate := "There are multiple matching sources; please choose one before I can return values."
			decision := e.checkCandidate(t.Context(), query, candidate, "", state, nil, false)
			if action == "recover" {
				require.Equal(t, candidateNeedSource, decision.Disposition)
			} else {
				require.Equal(t, candidateAccepted, decision.Disposition)
			}
			require.Len(t, model.calls, 1)
			require.Contains(t, model.calls[0][1].Content, "needs_clarification")
			require.Contains(t, model.calls[0][1].Content, "actual-a")
			require.Contains(t, model.calls[0][0].Content, "Never pick a default field")
			require.Equal(t, before, reviewDigest(state.RoundSteps), "recovery never fills selected_ids or bypasses the binder")
		})
	}
}

func TestDiscoveryDeliveryReviewClarificationRequiresActualNativeProtocolState(t *testing.T) {
	for _, scenario := range []string{"failed", "unknown_kind", "external_untyped", "successful_values"} {
		t.Run(scenario, func(t *testing.T) {
			_, state, _ := discoveryReviewFixture(t)
			result := &types.ToolResult{Success: true, Data: map[string]any{
				"_structured_execution": agenttools.StructuredOperationDescriptor{Operation: "analyze_table", EffectClass: "read"},
				"status":                "needs_clarification", "kind": "structured_source_binding",
			}}
			switch scenario {
			case "failed":
				result.Success = false
			case "unknown_kind":
				result.Data["kind"] = "external_text"
			case "external_untyped":
				result.Data["_structured_execution"] = map[string]any{"canonical_operation": "analyze_table", "effect_class": "read"}
			case "successful_values":
				result.Data["status"] = "ok"
			}
			state.RoundSteps[0].ToolCalls = append(state.RoundSteps[0].ToolCalls,
				types.ToolCall{Name: agenttools.ToolSearchKnowledge, Result: &types.ToolResult{Success: true}},
				types.ToolCall{Name: "structured_analyze_table", Result: result})
			require.Empty(t, discoveryOnlyObservations(state))
		})
	}
}

func TestDiscoveryDeliveryReviewDoesNotTriggerFromQuestionOrReadEvidence(t *testing.T) {
	for _, scenario := range []string{"no_tools", "failed_catalog", "display_only", "source_read", "unknown_integration", "strict", "budget_spent"} {
		t.Run(scenario, func(t *testing.T) {
			e, state, model := discoveryReviewFixture(t)
			switch scenario {
			case "no_tools":
				state.RoundSteps = nil
			case "failed_catalog":
				state.RoundSteps[0].ToolCalls[0].Result.Success = false
			case "display_only":
				state.RoundSteps[0].ToolCalls[0].Result.Data = nil
			case "source_read", "unknown_integration":
				name := agenttools.ToolReadDocument
				if scenario == "unknown_integration" {
					name = "external_source"
				}
				state.RoundSteps[0].ToolCalls = append(state.RoundSteps[0].ToolCalls, types.ToolCall{Name: name, Result: &types.ToolResult{Success: true}})
			case "strict":
				e.config.AnswerReviewMode = "strict"
			case "budget_spent":
				e.turnReview.Repairs = maxTurnCandidateRepairs
			}
			original := candidateDecision{Disposition: candidateAccepted, Candidate: "No matching data is available."}
			decision := e.checkDiscoveryOnlyDelivery(t.Context(), "Find a seasonal measure in any source.", original, state, nil)
			require.Equal(t, original, decision)
			require.Empty(t, model.calls)
		})
	}
}

func TestDiscoveryDeliveryReviewFailureIsBoundedAndNeverClaimsReview(t *testing.T) {
	for _, response := range []string{"bad-json", `{"action":"recover","feedback":""}`, `{"action":"keep","feedback":"expand scope"}`, `{"action":"keep","feedback":"","extra":true}`, `{"action":"keep","feedback":""} {}`} {
		t.Run(response, func(t *testing.T) {
			e, state, model := discoveryReviewFixture(t, deliveryReply(response, "stop", types.TokenUsage{}), deliveryReply(response, "stop", types.TokenUsage{}))
			original := candidateDecision{Disposition: candidateAccepted, Candidate: "Actual mapping status."}
			require.Equal(t, original, e.checkDiscoveryOnlyDelivery(t.Context(), "Report the status.", original, state, nil))
			require.NotNil(t, e.turnReview.DiscoveryReview)
			require.Empty(t, e.answerReviewFeedback)
			e.checkDiscoveryOnlyDelivery(t.Context(), "Report the status.", original, state, nil)
			require.Len(t, model.calls, 2, "only invalid schema gets one bounded protocol repair")
			require.Equal(t, "schema", e.turnReview.DiscoveryReview.FailureCode)
			require.Empty(t, e.turnReview.DiscoveryReview.Action)
			require.True(t, e.turnReview.DiscoveryReview.ProtocolRepairAttempted)
		})
	}
	e, state, model := discoveryReviewFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	original := candidateDecision{Disposition: candidateAccepted, Candidate: "Actual mapping status."}
	require.Equal(t, original, e.checkDiscoveryOnlyDelivery(ctx, "Report status.", original, state, nil))
	require.Nil(t, e.turnReview.DiscoveryReview)
	require.Empty(t, model.calls)
	// A real model transport error consumes the single attempt, not a retry loop.
	require.Equal(t, original, e.checkDiscoveryOnlyDelivery(t.Context(), "Report status.", original, state, nil))
	require.NotNil(t, e.turnReview.DiscoveryReview)
	e.turnReview.DiscoveryReview = nil
	state.RoundSteps[0].ToolCalls[0].Result.Data["pending_sheets"] = []any{map[string]any{"name": strings.Repeat("x", 40*1024)}}
	require.Equal(t, original, e.checkDiscoveryOnlyDelivery(t.Context(), "Report status.", original, state, nil))
	require.Nil(t, e.turnReview.DiscoveryReview, "oversized explicit evidence is not silently clipped")
}

type cancelledDiscoveryChat struct {
	mockChat
	callsStarted int
}

func (m *cancelledDiscoveryChat) ChatStream(ctx context.Context, messages []chat.Message, options *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	m.callsStarted++
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestDiscoveryDeliveryReviewHonorsParentDeadline(t *testing.T) {
	e, state, _ := discoveryReviewFixture(t)
	model := &cancelledDiscoveryChat{}
	e.chatModel = model
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	original := candidateDecision{Disposition: candidateAccepted, Candidate: "Actual mapping status."}
	require.Equal(t, original, e.checkDiscoveryOnlyDelivery(ctx, "Report status.", original, state, nil))
	require.Less(t, time.Since(start), time.Second)
	require.Equal(t, 1, model.callsStarted)
	require.NotNil(t, e.turnReview.DiscoveryReview)
	require.Empty(t, e.answerReviewFeedback)
}
