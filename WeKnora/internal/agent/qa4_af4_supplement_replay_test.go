package agent

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// Af3 KB-31 r1/r2 reached only the structured source. The third trace has a
// native document binding and read; the fixture restores only the report
// chunk from that original read, which the public event omitted.
func TestAf4WarehouseSupplementRecordedPublication(t *testing.T) {
	raw, err := os.ReadFile("testdata/qa4/replay/af4-kb31.json")
	require.NoError(t, err)
	var traces []json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &traces))
	require.Len(t, traces, 3)
	for _, frozen := range traces {
		var identity struct{ ID string }
		require.NoError(t, json.Unmarshal(frozen, &identity))
		for _, mode := range []string{"default", "Af5", "reversed", "paraphrased", "strict", "only", "same physical source"} {
			t.Run(identity.ID+"/"+mode, func(t *testing.T) {
				t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "false")
				t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "true")
				t.Setenv("WEKNORA_AGENT_RELAXED_TERMINAL_EVIDENCE", "")
				t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "true")
				t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
				t.Setenv("WEKNORA_AGENT_STRUCTURED_TASK_PLAN", "true")
				t.Setenv("WEKNORA_AGENT_FINAL_READ_ANSWER", "false")
				if mode == "strict" {
					t.Setenv("WEKNORA_AGENT_RELAXED_TERMINAL_EVIDENCE", "false")
				}
				if mode == "Af5" {
					t.Setenv("WEKNORA_AGENT_FINAL_READ_ANSWER", "true")
				}
				var trace struct {
					ID, Question, Final string
					Steps               []types.AgentStep
					Drafts              []string
				}
				hadSupplement := strings.HasPrefix(identity.ID, "qa4-Af3-3")
				input := frozen
				if hadSupplement && mode == "same physical source" {
					// A document access alias for the workbook must not be
					// counted as an independent supplement.
					input = []byte(strings.ReplaceAll(string(frozen),
						"eb0ac8ba-213f-40c3-a8d6-0b70b3c969e5", "96c5719b-5dd5-444b-bfbb-59b30fbf3f53"))
				}
				require.NoError(t, json.Unmarshal(input, &trace))
				e := newTestEngine(t, &qa4Stage2CModel{t: t}, withCitationsEnabled(true))
				e.config.WorkbenchDefault = true
				e.config.AnswerReviewMode = "standard"
				e.config.EvidenceReferencesEnabled = true
				e.config.SearchTargets = types.SearchTargets{{
					Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "0d463b67-5640-4b94-84d1-8bbb426c3bcd", TenantID: 1,
				}}
				e.toolRegistry = agenttools.NewToolRegistry()
				base := agenttools.NewStructuredDataTool()
				e.toolRegistry.RegisterTool(base)
				e.toolRegistry.PrepareStructuredProtocol()
				manifest := structuredCodecManifest()
				for _, operation := range sourceNavigationRows(manifest["tools"]) {
					if operation["name"] == "analyze_table" || operation["name"] == "query_series" {
						coverageObject(coverageObject(operation["inputSchema"])["properties"])["include_last_valid"] = map[string]any{"type": "boolean"}
					}
				}
				for _, tool := range agenttools.NewStructuredReadOperationTools(base, manifest) {
					e.toolRegistry.RegisterTool(tool)
				}
				documents := &sourceTimeDocumentStore{byID: map[string]*types.Knowledge{}}
				for _, step := range trace.Steps {
					for _, call := range step.ToolCalls {
						if call.Name != agenttools.ToolBindDocumentScope {
							continue
						}
						for _, row := range sourceNavigationRows(call.Result.Data["documents"]) {
							id := sourceNavigationString(row, "knowledge_id")
							documents.byID[id] = &types.Knowledge{
								ID: id, KnowledgeBaseID: sourceNavigationString(row, "knowledge_base_id"),
								Title: sourceNavigationString(row, "title"), FileName: sourceNavigationString(row, "file_name"),
								TenantID: 1, ParseStatus: types.ParseStatusCompleted,
							}
						}
					}
				}
				chunks := &qa4Stage2CChunks{repo: &qa4Stage2CChunkRepository{}}
				e.toolRegistry.RegisterTool(agenttools.NewReadDocumentTool(documents, chunks, e.config.SearchTargets))
				e.toolRegistry.RegisterTool(agenttools.NewSearchKnowledgeTool(&sourceDiscoveryKnowledgeService{}, documents, chunks, e.config.SearchTargets, nil, nil))
				agenttools.PrepareDocumentScope(e.toolRegistry, trace.Question)

				ctx := withAgentExecutionStart(t.Context())
				state := &types.AgentState{}
				messages := []chat.Message{{Role: "user", Content: trace.Question}}
				e.reviewConversation, e.reviewConversationReady = conversationThroughLastHuman(messages), true
				for _, step := range trace.Steps {
					for i := range step.ToolCalls {
						call := &step.ToolCalls[i]
						args, err := json.Marshal(call.Args)
						require.NoError(t, err)
						if call.Name == agenttools.ToolBindDocumentScope {
							binder, err := e.toolRegistry.GetTool(call.Name)
							require.NoError(t, err)
							result, err := binder.Execute(ctx, args)
							require.NoError(t, err)
							require.True(t, result.Success, result.Error)
						}
						if _, exists := call.Result.Data["_structured_execution"]; exists {
							descriptor, _, native := e.toolRegistry.StructuredInvocation(call.Name, args)
							require.True(t, native)
							call.Result.Data["_structured_execution"] = descriptor
						}
					}
				}
				if mode == "reversed" {
					slices.Reverse(trace.Steps)
				}
				for _, step := range trace.Steps {
					state.CurrentRound = max(state.CurrentRound, step.Iteration)
					state.RoundSteps = append(state.RoundSteps, step)
					messages = e.appendToolResults(messages, step)
				}
				assertAf4SupplementHandoff(t, e, ctx, state, messages, trace.Question, identity.ID, mode)
				for _, previous := range trace.Drafts {
					if mode == "paraphrased" {
						previous = "《棉花周报20240310》在第 18 页披露，该周郑棉仓单合计为 60.6 万吨。"
					}
					state.RoundSteps = append(state.RoundSteps, types.AgentStep{Thought: previous})
				}
				if mode == "only" {
					e.taskPlan = &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{{RequestBasis: trace.Question, StructuredTaskGoal: &StructuredTaskGoal{
						SourceMode: "only", RestrictionBasis: "棉花数据库", PrimaryRef: "棉花数据库",
					}}}}
				}
				e.turnReview.DeferTerminalClose = true
				primary, _, _ := strings.Cut(trace.Final, "\n\n同期《")
				candidate := e.checkCandidate(ctx, trace.Question, primary, "", state, messages, false)
				e.commitTerminalResult(ctx, state, "session", candidate.Candidate, "", "answer", false)
				var complete event.AgentCompleteData
				e.eventBus.On(event.EventAgentComplete, func(_ context.Context, published event.Event) error {
					complete = published.Data.(event.AgentCompleteData)
					return nil
				})
				e.emitCompletionEvent(ctx, state, "session", "turn", time.Now())
				require.Equal(t, state.FinalAnswer, complete.FinalAnswer)
				require.Contains(t, state.FinalAnswer, "5160")
				require.Contains(t, state.FinalAnswer, "B3340")
				require.True(t, coverageTextHasDate(state.FinalAnswer, "2023-09-21"))
				require.NotContains(t, state.FinalAnswer, "<kb")
				require.NotContains(t, state.FinalAnswer, "<evidence")
				if hadSupplement && (mode == "default" || mode == "Af5" || mode == "reversed" || mode == "paraphrased") {
					require.Equal(t, 1, strings.Count(state.FinalAnswer, "60.6"))
					require.Contains(t, state.FinalAnswer, "万吨")
					require.Contains(t, state.FinalAnswer, "棉花周报20240310")
					require.Contains(t, state.FinalAnswer, "第 18 页")
					return
				}
				require.NotContains(t, state.FinalAnswer, "60.6")
			})
		}
	}
}

func assertAf4SupplementHandoff(t *testing.T, e *AgentEngine, ctx context.Context, state *types.AgentState, messages []chat.Message, question, identity, mode string) {
	t.Helper()
	// Exercise existing runtime feedback with an explicit named-first plan.
	// The historical logs retain the discovery channels but truncate the
	// complete typed coverage goal. No historical plan or read is inferred.
	plannedDocuments := !strings.HasPrefix(identity, "qa4-Af3-2")
	domains := []string{"structured"}
	if plannedDocuments {
		domains = append(domains, "knowledge")
	}
	goal := &StructuredTaskGoal{
		SourceMode: "named_first", Table: "郑棉仓单", VersionTask: "single",
		Period: map[string]any{"from": "2024-03-10", "to": "2024-03-10", "grain": "day"},
	}
	if mode == "only" {
		goal.SourceMode, goal.PrimaryRef, goal.RestrictionBasis = "only", "棉花数据库", "棉花数据库"
	}
	e.taskPlan = &taskEvidencePlan{
		SourceDiscovery: &taskSourceDiscovery{Mode: "discover", SourceDomains: domains},
		CoverageTargets: []taskCoverageTarget{{RequestBasis: question, StructuredTaskGoal: goal}},
	}
	e.refreshEvidenceCompletion(ctx, question, state, messages)
	feedback := e.lastValidCoverageFeedback(ctx)
	if mode != "only" {
		require.NotEmpty(t, feedback)
	}
	wantPending := plannedDocuments && mode != "strict" && mode != "only" &&
		(!strings.HasPrefix(identity, "qa4-Af3-3") || mode == "same physical source")
	require.Equal(t, wantPending, strings.Contains(lastValidFeedbackInstructions(feedback), "supplement_pending"))
	e.taskPlan = nil
	e.evidenceCompletion = newEvidenceCompletionTurn(0)
}
