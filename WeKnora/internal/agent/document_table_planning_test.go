package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestDocumentTablePlanningRetainsValidatedGoalsAndScope(t *testing.T) {
	for _, enabled := range []string{"true", "false"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", enabled)
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "false")
			const question = "Use only North record and South record for the monthly output comparison. Check the inventory table separately."
			model := &taskPlannerChat{}
			e := newTestEngine(t, model)
			e.config.WorkbenchDefault, e.config.TaskPlanningEnabled, e.config.AnswerReviewMode = true, true, "standard"
			e.toolRegistry = agenttools.NewToolRegistry()
			e.toolRegistry.RegisterTool(newCountingTool(agenttools.ToolReadDocument))
			e.prepareTaskPlanning()
			e.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "library", TenantID: 7}}
			e.selectedDocs = []*SelectedDocumentInfo{{KnowledgeID: "known", KnowledgeBaseID: "library"}}
			known := e.modelContext.RegisterDocument("known")
			model.plan = &types.ChatResponse{FinishReason: "stop", Content: fmt.Sprintf(`{
				"protocol_version":1,"goals":["Compare output","Check inventory"],"evidence":["Original tables"],
				"tool_names":["read_document","unavailable"],"source_choices":[44],
				"completion_criteria":["Attribute each value"],
				"source_discovery":{"mode":"recover","query":"unvalidated acquisition","source_domains":["knowledge"]},
				"coverage_targets":[
					{"goal_index":1,"request_basis":"Check the inventory table separately.","source_mode":"open","version_task":"single","table":"Inventory","comparison_sources":[42]},
					{"goal_index":0,"request_basis":%q,"source_mode":"only","restriction_basis":"Use only North record and South record",
					 "primary_ref":"doc://999","version_task":"compare_sources","table":"Output","metric":"Volume",
					 "period":{"grain":"month","from":"2025-01-01","to":"2025-01-31"},"comparison_sources":[%q,"doc://999"],"completed":true},
					{"goal_index":9,"source_mode":"open"}
				]}`, question, known)}
			messages := []chat.Message{{Role: "system", Content: "Ordinary instructions."}}
			planned := e.planWorkbenchTask(t.Context(), &types.AgentState{}, question, messages, "test")
			if enabled == "false" {
				require.Nil(t, e.taskPlan)
				require.Equal(t, messages, planned)
				return
			}
			require.NotNil(t, e.taskPlan)
			require.Nil(t, e.taskProjection, "invalid advice must leave the full authorized tools available")
			require.NotContains(t, planned[0].Content, "doc://999")
			require.Contains(t, planned[0].Content, "unavailable_tool")
			require.Contains(t, planned[0].Content, "unavailable_source_choice")
			require.Contains(t, planned[0].Content, "invalid_source_discovery")
			require.NotContains(t, planned[0].Content, "unvalidated acquisition")
			require.Contains(t, planned[0].Content, "unverified_comparison_source")
			require.Len(t, e.taskPlan.CoverageTargets, 2)
			single, comparison := e.taskPlan.CoverageTargets[0], e.taskPlan.CoverageTargets[1]
			require.Equal(t, "Inventory", single.Table)
			require.False(t, plannedTableComparison(single))
			require.True(t, plannedTableComparison(comparison))
			require.Equal(t, 1, comparison.UnresolvedComparisonSources)
			require.Equal(t, []string{known}, comparison.ComparisonSources)
			require.Empty(t, comparison.PrimaryRef)
			require.Equal(t, "Use only North record and South record", comparison.RestrictionBasis)
			require.Len(t, e.verifiedCoverageScopePins(t.Context(), comparison).sources, 1)
		})
	}
}

func TestDocumentTablePlanningSingleAndQualitativeGoals(t *testing.T) {
	for _, enabled := range []string{"true", "false"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", enabled)
			plan := &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{{RequestBasis: "Read the inventory table",
				StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "open", VersionTask: "single", Table: "Inventory"}}}}
			err := validateTaskCoverageBasis(plan, "Read the inventory table", nil, []string{"Read the inventory table"}, nil)
			if enabled == "false" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Empty(t, plan.CoveragePlanIssues)
			}
			qualitative := &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{{RequestBasis: "Compare what was reported",
				StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "open", VersionTask: "compare_sources"}}}}
			require.NoError(t, validateTaskCoverageBasis(qualitative, "Compare what was reported", nil, []string{"Compare what was reported"}, nil))
			require.Len(t, qualitative.CoverageTargets, 1)
			require.False(t, plannedTableComparison(qualitative.CoverageTargets[0]))
		})
	}
}

func TestDocumentTablePlanningConflictingGoalCannotWidenAnExplicitRestriction(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	const question = "Use only the selected source before checking another claim."
	pinned := func(ref string) bool { return ref == "doc://1" || ref == "doc://2" }
	valid := taskCoverageTarget{RequestBasis: question, StructuredTaskGoal: &StructuredTaskGoal{
		SourceMode: "only", RestrictionBasis: "doc://1", PrimaryRef: "doc://2"}}
	conflicting := taskCoverageTarget{RequestBasis: question, StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "open"}}
	reversed := taskCoverageTarget{RequestBasis: question, StructuredTaskGoal: &StructuredTaskGoal{
		SourceMode: "only", RestrictionBasis: "doc://2", PrimaryRef: "doc://1"}}
	for _, entries := range [][]taskCoverageTarget{{valid, conflicting}, {valid, reversed}} {
		for range 2 {
			plan := &taskEvidencePlan{CoverageTargets: slices.Clone(entries)}
			require.NoError(t, validateTaskCoverageBasis(plan, question, nil, []string{question}, pinned))
			require.Len(t, plan.CoverageTargets, 1)
			target := plan.CoverageTargets[0]
			require.Equal(t, "only", target.SourceMode)
			require.Empty(t, target.PrimaryRef, "a conflicting pin cannot become the preserved primary source")
			if entries[0].SourceMode == "open" || entries[1].SourceMode == "open" {
				require.Equal(t, "doc://1", target.RestrictionBasis)
			} else {
				require.Empty(t, target.RestrictionBasis)
				require.Contains(t, target.PlanIssues, "ambiguous_only_goal")
			}
			slices.Reverse(entries)
		}
	}
}

func TestDocumentTableRawDeclarationWithoutPlanKeepsIndependentlyVerifiedScope(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "false")
	const question = "Use only the selected update to check the Output table."
	const library = "scope-library"
	ctx := types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer})
	e := newTestEngine(t, &mockChat{})
	e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
	e.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: library, TenantID: 7}}
	e.selectedDocs = []*SelectedDocumentInfo{{KnowledgeID: "known", KnowledgeBaseID: library}}
	e.toolRegistry = agenttools.NewToolRegistry()
	e.toolRegistry.RegisterTool(agenttools.NewReadDocumentTool(nil, nil, e.config.SearchTargets))
	known := e.modelContext.RegisterDocument("known")
	state := &types.AgentState{}
	calls := []types.LLMToolCall{{Function: types.FunctionCall{Name: agenttools.ToolReadDocument, Arguments: fmt.Sprintf(
		`{"id":"outside","task_coverage":{"goal_index":0,"role":"primary","selection_ref":"invented",
			 "goal":{"goal_index":0,"request_basis":%q,"source_mode":"only","restriction_basis":%q,
			         "primary_ref":"doc://999","table":false,"version_task":"single"}}}`, question, known)}}}
	e.processEvidenceCompletionCalls(ctx, question, state, nil, calls)
	require.Nil(t, calls[0].CoverageGoalIndex, "the optional receipt was forged")
	require.NotContains(t, calls[0].Function.Arguments, coverageArgument)
	targets := e.evidenceCompletionTargets()
	require.Len(t, targets, 1)
	require.Equal(t, known, targets[0].RestrictionBasis)
	require.Empty(t, targets[0].PrimaryRef)
	require.False(t, e.coverageSourceCallAllowed(ctx, calls[0], map[string]any{"id": "outside"}))
	require.NotContains(t, e.evidenceCompletionNotice(), "doc://999")
}

func TestDocumentTablePartialScopeRequiresNativeSelectionAfterRepair(t *testing.T) {
	for _, shuffled := range []bool{false, true} {
		t.Run(fmt.Sprint(shuffled), func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "false")
			const question = "Use only North record and South record to compare the same monthly output table."
			const library = "scope-library"
			ctx := types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer})
			e := newTestEngine(t, &mockChat{})
			e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
			e.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: library, TenantID: 7}}
			records := &coverageScopeDocuments{sourceTimeDocumentStore: &sourceTimeDocumentStore{byID: make(map[string]*types.Knowledge)}}
			names := map[string]string{"north": "Quarterly attachment A", "south": "Edition Z", "outside": "Edition Z"}
			order := []string{"north", "south", "outside"}
			if shuffled {
				slices.Reverse(order)
			}
			for _, id := range order {
				doc := &types.Knowledge{ID: id, KnowledgeBaseID: library, TenantID: 7, Title: names[id], FileName: id + ".pdf",
					FileType: "pdf", FileHash: strings.Repeat("a", 64), ParseStatus: types.ParseStatusCompleted}
				records.documents = append(records.documents, doc)
				records.byID[id] = doc
			}
			repository := &coverageScopeChunks{}
			e.toolRegistry = agenttools.NewToolRegistry()
			e.toolRegistry.RegisterTool(agenttools.NewReadDocumentTool(records, &coverageScopeChunkService{repository: repository}, e.config.SearchTargets))
			e.toolRegistry.RegisterTool(agenttools.NewListDocumentsTool(records, e.config.SearchTargets))
			agenttools.PrepareDocumentScope(e.toolRegistry, question)
			e.selectedDocs = []*SelectedDocumentInfo{{KnowledgeID: "north", KnowledgeBaseID: library}}
			pinned := e.modelContext.RegisterDocument("north")
			base := &taskEvidencePlan{Goals: []string{"Compare original values"}, Evidence: []string{"Original table"}}
			base.CoverageTargets = []taskCoverageTarget{{RequestBasis: question, StructuredTaskGoal: &StructuredTaskGoal{
				SourceMode: "only", RestrictionBasis: question, VersionTask: "compare_sources", Table: "Output", Metric: "Volume",
				Period: map[string]any{"grain": "month"}, ComparisonSources: []string{pinned, "doc://999"}}}}
			require.NoError(t, validateTaskCoverageBasis(base, question, nil, []string{question}, e.coveragePinnedSelection))
			e.taskPlan = base
			state := &types.AgentState{}
			invoke := func(id, name string, input map[string]any) types.ToolCall {
				t.Helper()
				raw, err := json.Marshal(input)
				require.NoError(t, err)
				calls := []types.LLMToolCall{{ID: id, Function: types.FunctionCall{Name: name, Arguments: string(raw)}}}
				e.processEvidenceCompletionCalls(ctx, question, state, nil, calls)
				call := e.runToolCall(ctx, calls[0], 0, 0, 0, "session", "turn")
				state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{call}})
				e.refreshEvidenceCompletion(ctx, question, state, nil)
				return call
			}
			bind := func(id string, ids ...string) types.ToolCall {
				var sources []map[string]string
				for _, source := range ids {
					sources = append(sources, map[string]string{"knowledge_id": source, "expected_file_name": source + ".pdf"})
				}
				return invoke(id, agenttools.ToolBindDocumentScope, map[string]any{"sources": sources, "selection_reason": "Verified the selected directory identities."})
			}
			read := func(callID, id string, declaration any) types.ToolCall {
				args := map[string]any{"id": id}
				if declaration != nil {
					args[coverageArgument] = declaration
				}
				return invoke(callID, agenttools.ToolReadDocument, args)
			}
			require.True(t, invoke("directory", agenttools.ToolListDocuments, map[string]any{"knowledge_base_id": library}).Result.Success)
			require.True(t, bind("known-binding", "north").Result.Success)
			require.True(t, read("known-side", "north", nil).Result.Success)
			require.Equal(t, []string{"north"}, repository.reads)
			require.Contains(t, e.evidenceCompletionNotice(), "comparison_sources_unresolved")
			require.Equal(t, coverageScopeRejection, bind("unverified-side", "north", "south").Result.Error)
			repair := map[string]any{"goal_index": 0, "goal": map[string]any{
				"goal_index": 0, "source_mode": "only", "request_basis": question, "comparison_sources": []string{"South record"},
			}}
			require.Equal(t, coverageScopeRejection, read("early-read", "south", repair).Result.Error, "a user-anchored declaration cannot authorize content")
			require.Equal(t, 0, e.evidenceCompletionTargets()[0].UnresolvedComparisonSources)
			require.Equal(t, []string{pinned, "South record"}, e.evidenceCompletionTargets()[0].ComparisonSources)
			require.Equal(t, "Output", e.evidenceCompletionTargets()[0].Table)
			require.Equal(t, coverageScopeRejection, bind("discarding-first", "south").Result.Error)
			parts := []string{"north", "south"}
			if shuffled {
				slices.Reverse(parts)
			}
			continued := bind("bound-both", parts...)
			require.True(t, continued.Result.Success, continued.Result.Error)
			require.True(t, read("missing-side", "south", nil).Result.Success)
			require.Equal(t, coverageScopeRejection, read("outside", "outside", map[string]any{"goal_index": 0, "selection_ref": "forged"}).Result.Error)
			require.Equal(t, []string{"north", "south"}, repository.reads)
			require.True(t, slices.ContainsFunc(e.evidenceCompletion.record.Evidence, func(item coverageEvidence) bool {
				return item.ToolCallID == "known-side" && item.Bound && item.Kind == "body"
			}), "native binding must preserve previously completed authorized reads")
		})
	}
}
