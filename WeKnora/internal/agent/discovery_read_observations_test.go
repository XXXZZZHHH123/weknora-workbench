package agent

import (
	"encoding/json"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func discoveryReadFixture(t *testing.T, action, feedback string) (*AgentEngine, *types.AgentState, *mockChat) {
	e := navigationTestEngine(t)
	e.config.AnswerReviewMode = "standard"
	model := &mockChat{responses: []mockResponse{discoveryReply(action, feedback)}}
	e.chatModel = model
	call := navigationCall(agenttools.ToolReadDocument, map[string]any{
		"knowledge_id": "actual-report", "knowledge_title": "Selected report", "page": 7, "page_complete": false,
		"chunks":        []map[string]any{{"chunk_id": "actual-part", "knowledge_id": "actual-report", "content": "Requested ratio chart is referenced in original figure A; the neighboring absolute-measure figure B has separate geometry."}},
		"image_catalog": []map[string]any{{"reference": "resource://actual-figure", "page": 7, "label": "Source figure", "read_args": map[string]any{"images": []string{"resource://actual-figure"}, "image_mode": "geometry"}}},
	})
	return e, &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}, model
}

func TestDiscoveryReadDeliveryUsesOriginalTaskAndKeepsLegitimateLimits(t *testing.T) {
	for _, scenario := range []struct{ name, question, candidate, action, feedback string }{
		{"qualitative_recovery", "Describe the seasonal shape in the available chart.", "First import complete monthly records; without full monthly means I cannot describe the shape.", "recover", "The user asks for qualitative shape, not monthly statistics. Inspect the original figure A already identified in the source navigation, preserving its ratio definition and uncertainty."},
		{"substantive_keep", "Describe the chart qualitatively.", "The visible paths differ by period; a fixed common seasonal pattern is not supported. This is a qualitative reading of the shown chart.", "keep", ""},
		{"exact_restricted_keep", "Use only this file's exact original monthly observations to compute the full-period average; do not estimate from a chart.", "The selected file supplies only an original chart, so the requested exact average cannot be computed from its observed metadata.", "keep", ""},
		{"neighbor_not_authority", "Explain the ratio chart.", "The other figure's absolute measure cannot establish the requested ratio's shape.", "recover", "Inspect the ratio's separately identified original figure A; do not use the neighboring absolute-measure geometry as ratio evidence."},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			e, state, model := discoveryReadFixture(t, scenario.action, scenario.feedback)
			e.taskPlan.Goals = []string{"Compute full monthly means and compare each component separately."}
			before := reviewDigest(state.RoundSteps)
			decision := e.checkCandidate(t.Context(), scenario.question, scenario.candidate, "", state, nil, false)
			if scenario.action == "recover" {
				require.Equal(t, candidateNeedSource, decision.Disposition)
			} else {
				require.Equal(t, candidateAccepted, decision.Disposition)
			}
			require.Equal(t, scenario.candidate, decision.Candidate)
			require.Equal(t, before, reviewDigest(state.RoundSteps))
			require.Len(t, model.calls, 1)
			require.Contains(t, model.calls[0][1].Content, scenario.question)
			require.Contains(t, model.calls[0][1].Content, "actual_source_read_progress")
			require.Contains(t, model.calls[0][1].Content, "resource://actual-figure")
			require.Contains(t, model.calls[0][0].Content, "Never substitute a neighboring metric")
			require.Contains(t, model.calls[0][0].Content, "not with extra statistical precision")
			require.False(t, decision.SourceReviewAttempted)
			require.Empty(t, model.opts[0].Tools)
			require.Zero(t, e.turnReview.Repairs)
		})
	}
}

func TestDiscoveryReadObservationsRequireActualNativeExecutionAndBoundProjection(t *testing.T) {
	e, state, _ := discoveryReadFixture(t, "keep", "")
	view := e.discoveryReadObservations(state)
	require.NotNil(t, view)
	require.True(t, view["navigation_only"].(bool))
	require.False(t, view["task_coverage_established"].(bool))
	state.RoundSteps[0].ToolCalls[0].Result.Success = false
	require.Nil(t, e.discoveryReadObservations(state))
	state.RoundSteps[0].ToolCalls[0].Result.Success = true
	e.toolRegistry = agenttools.NewToolRegistry()
	e.toolRegistry.RegisterTool(newCountingTool(agenttools.ToolReadDocument))
	require.Nil(t, e.discoveryReadObservations(state), "same name with external/non-native registration is insufficient")
	// Actual private typed reads qualify; a JSON lookalike never qualifies.
	data := map[string]any{"status": "ok", "kind": "structured_analysis", "rows": []any{[]any{987654.321}}, "source": map[string]any{"knowledge_id": "actual-source"}, "_structured_execution": agenttools.StructuredOperationDescriptor{Operation: "analyze_table", EffectClass: "read"}}
	call := navigationCall("structured_analyze_table", data)
	state.RoundSteps = []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
	view = e.discoveryReadObservations(state)
	require.NotNil(t, view)
	raw, err := json.Marshal(view)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "987654.321", "source result rows do not become a second numeric review pool")
	data["_structured_execution"] = map[string]any{"canonical_operation": "analyze_table", "effect_class": "read"}
	require.Nil(t, e.discoveryReadObservations(state))
	data["_structured_execution"] = agenttools.StructuredOperationDescriptor{Operation: "analyze_table", EffectClass: "read"}
	data["source"] = map[string]any{"knowledge_id": strings.Repeat("oversized", 1000)}
	for i := 0; i < 20; i++ {
		state.RoundSteps[0].ToolCalls = append(state.RoundSteps[0].ToolCalls, call)
	}
	view = e.discoveryReadObservations(state)
	raw, err = json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(raw), 12*1024)
	require.Equal(t, 13, view["omitted_observations"])
	require.Contains(t, string(raw), "source_omitted")
}

func TestDiscoveryReadDeliverySharesReceiptBudgetAndSkipsNoSourceConcept(t *testing.T) {
	e, state, model := discoveryReadFixture(t, "recover", "Inspect the actually discovered original figure for the qualitative explanation.")
	original := candidateDecision{Disposition: candidateAccepted, Candidate: "Full monthly observations must be imported first.", CandidateHash: "candidate", TaskHash: "task", EvidenceHash: "source", SourceScopeHash: "scope"}
	decision := e.checkDiscoveryOnlyDelivery(t.Context(), "Describe the qualitative shape.", original, state, nil)
	require.Equal(t, candidateNeedSource, decision.Disposition)
	e.turnReview.Repairs = maxTurnCandidateRepairs
	repeated := e.checkDiscoveryOnlyDelivery(t.Context(), "Describe the qualitative shape.", original, state, nil)
	require.Equal(t, candidateNeedSource, repeated.Disposition)
	require.Len(t, model.calls, 1)
	e, state, model = discoveryReadFixture(t, "keep", "")
	state.RoundSteps = nil
	require.Equal(t, original, e.checkDiscoveryOnlyDelivery(t.Context(), "Explain what seasonal variation means.", original, state, nil))
	require.Empty(t, model.calls)
	// Existing input/transport/cancellation limits remain the same shared path.
	e, state, model = discoveryReadFixture(t, "keep", "")
	e.turnReview.Repairs = maxTurnCandidateRepairs
	require.Equal(t, original, e.checkDiscoveryOnlyDelivery(t.Context(), "Explain the source.", original, state, nil))
	require.Empty(t, model.calls)
}

func TestDiscoveryReadImageProjectionKeepsRuntimeTypedCatalogAndActualStatus(t *testing.T) {
	// Same native JSON shape as tools.documentImage (a private struct), rather
	// than the []map that only appears after history JSON decoding.
	type imageEntry struct {
		Reference   string         `json:"reference"`
		KnowledgeID string         `json:"knowledge_id"`
		SourcePart  string         `json:"source_part"`
		Page        int            `json:"page"`
		ReadArgs    map[string]any `json:"read_args"`
		Context     string         `json:"source_context"`
	}
	rows := []imageEntry{{Reference: "resource://actual", KnowledgeID: "actual-source", SourcePart: "native/figure.emf", Page: 7, ReadArgs: map[string]any{"images": []string{"resource://actual"}, "image_mode": "geometry"}}}
	rows[0].Context = strings.Repeat("真实长上下文", 2000)
	view := discoveryImageNavigation(rows)
	raw, err := json.Marshal(view)
	require.NoError(t, err)
	require.Contains(t, string(raw), "resource://actual")
	require.Contains(t, string(raw), "native/figure.emf")
	require.Contains(t, string(raw), "read_args")
	require.NotContains(t, string(raw), "read_success", "a catalog does not establish an actual read")
	require.Equal(t, 0, view["omitted_rows"])
	status := discoveryImageNavigation([]map[string]any{{"reference": "resource://actual", "read_success": false, "geometry_read_success": true}})
	samples := sourceNavigationRows(status["samples"])
	require.Equal(t, false, samples[0]["read_success"])
	require.Equal(t, true, samples[0]["geometry_read_success"])
	for i := 0; i < 10; i++ {
		rows = append(rows, rows[0])
	}
	require.Equal(t, 7, discoveryImageNavigation(rows)["omitted_rows"])
	rows[0].Reference = strings.Repeat("oversized", 1000)
	oversized := discoveryImageNavigation(rows)
	require.Equal(t, 7, oversized["omitted_rows"])
	require.Equal(t, true, sourceNavigationRows(oversized["samples"])[0]["reference_omitted"])
}
