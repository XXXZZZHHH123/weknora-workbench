package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
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

func configureAf5Publication(t *testing.T, enabled bool) {
	t.Helper()
	t.Setenv("WEKNORA_AGENT_FINAL_READ_ANSWER", fmt.Sprint(enabled))
	t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "true")
	t.Setenv("WEKNORA_AGENT_RELAXED_TERMINAL_EVIDENCE", "true")
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "false")
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	t.Setenv("WEKNORA_AGENT_STRUCTURED_TASK_PLAN", "true")
	t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "true")
	t.Setenv("STRUCTURED_DATA_CATALOG_MANIFEST_REUSE", "true")
}

type af5ComparisonReplay struct {
	af3KB28Replay
	Documents []*types.Knowledge
	Target    *taskCoverageTarget
	RequestID string
}

// Public events omit some page body text. R1 needs only its recorded table
// navigation, returned image description and native worksheet row. R2 and R3
// retain their original published bodies. No source is fetched during replay.
func loadAf5ComparisonReplays(t *testing.T) []af5ComparisonReplay {
	t.Helper()
	raw, err := os.ReadFile("testdata/qa4/replay/af5-kb28.json.gz")
	require.NoError(t, err)
	file, err := gzip.NewReader(bytes.NewReader(raw))
	require.NoError(t, err)
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	var traces []af5ComparisonReplay
	require.NoError(t, decoder.Decode(&traces))
	require.Len(t, traces, 3)
	require.Equal(t, "e1cde16b-61af-48b3-b839-c58b9411e060", traces[0].RequestID)
	return traces
}

func newAf5ComparisonEngine(t *testing.T, trace af5ComparisonReplay, reversed bool) (*AgentEngine, *types.AgentState, context.Context) {
	t.Helper()
	e, state, ctx, messages := newAf3KB28ReplayEngine(t, trace.af3KB28Replay, &mockChat{})
	if trace.Target != nil {
		e.taskPlan.CoverageTargets = []taskCoverageTarget{*trace.Target}
	}
	ctx = context.WithValue(ctx, types.LanguageContextKey, "zh-CN")
	store := &sourceTimeDocumentStore{byID: map[string]*types.Knowledge{}}
	for _, doc := range trace.Documents {
		store.byID[doc.ID] = doc
	}
	targets := e.config.SearchTargets
	e.toolRegistry = agenttools.NewToolRegistry()
	base := agenttools.NewStructuredDataTool().WithSearchTargets(targets)
	e.toolRegistry.RegisterTool(base)
	e.toolRegistry.PrepareStructuredProtocol()
	manifest := structuredCodecManifest()
	for _, entry := range sourceNavigationRows(manifest["tools"]) {
		fields := coverageObject(coverageObject(entry["inputSchema"])["properties"])
		for _, field := range []string{"start_date", "end_date", "series_ids", "operation", "source_binding", "source_question", "sql"} {
			fields[field] = map[string]any{}
		}
	}
	for _, tool := range agenttools.NewStructuredReadOperationTools(base, manifest) {
		e.toolRegistry.RegisterTool(tool)
	}
	e.toolRegistry.RegisterTool(agenttools.NewReadDocumentTool(store, nil, targets))
	e.toolRegistry.RegisterTool(agenttools.NewSearchKnowledgeTool(nil, store, nil, targets, nil, nil))
	agenttools.PrepareDocumentScope(e.toolRegistry, trace.Question)
	binder, err := e.toolRegistry.GetTool(agenttools.ToolBindDocumentScope)
	require.NoError(t, err)
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if call.Name == agenttools.ToolBindDocumentScope && call.Result.Success {
				args, err := json.Marshal(call.ExecutionArgs())
				require.NoError(t, err)
				result, err := binder.Execute(ctx, args)
				require.NoError(t, err)
				require.True(t, result.Success, result.Error)
			}
		}
	}
	if reversed {
		slices.Reverse(state.RoundSteps)
		for i := range state.RoundSteps {
			slices.Reverse(state.RoundSteps[i].ToolCalls)
			for _, call := range state.RoundSteps[i].ToolCalls {
				if call.Name == agenttools.ToolReadDocument && call.Result != nil {
					if candidates, ok := call.Result.Data["table_candidates"].([]any); ok {
						slices.Reverse(candidates)
					}
					if returned, ok := call.Result.Data["image_evidence"].([]any); ok {
						slices.Reverse(returned)
					}
				}
			}
		}
	}
	e.turnReview.FinalCoverageReadAttempted = true
	e.turnReview.DeferTerminalClose = true
	e.refreshEvidenceCompletion(ctx, trace.Question, state, messages)
	return e, state, ctx
}

func TestQA4Af5ComparisonFromRecordedVisualRead(t *testing.T) {
	for _, test := range []struct {
		name      string
		enabled   bool
		reversed  bool
		final     string
		outcome   string
		failure   terminalFailure
		wrongPage bool
		side      string
		approved  bool
	}{
		{name: "Af4", outcome: "failed", failure: terminalTableEvidence},
		{name: "recorded stop", enabled: true, outcome: "failed", failure: terminalTableEvidence},
		{name: "reordered", enabled: true, reversed: true, outcome: "failed", failure: terminalTableEvidence},
		{name: "partial", enabled: true, final: "尚缺同表、同期读数。", outcome: "evidence_partial"},
		{name: "answer without readings", enabled: true, final: "尚缺同表、同期读数。", outcome: "answer"},
		{name: "two approved readings without conclusion", enabled: true, final: "棉花周报20240331.pptx 第 6 页 工商业期末库存 331.2。棉花数据库.xlsx 工商业期末库存 G87 298.93。", outcome: "answer", approved: true},
		{name: "only report read", enabled: true, outcome: "failed", failure: terminalTableEvidence, side: "report"},
		{name: "only workbook read", enabled: true, outcome: "failed", failure: terminalTableEvidence, side: "workbook"},
		{name: "different image page", enabled: true, outcome: "failed", failure: terminalTableEvidence, wrongPage: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			configureAf5Publication(t, test.enabled)
			trace := loadAf5ComparisonReplays(t)[0]
			e, state, ctx := newAf5ComparisonEngine(t, trace, test.reversed)
			if !test.enabled {
				// Public events omit the rejected draft. The retained workbook
				// paragraph in the published Af4 answer selects the original cell.
				e.turnReview.Last.Candidate, _, _ = strings.Cut(trace.Final, "\n\n")
			}
			for i, step := range state.RoundSteps {
				if test.side == "report" {
					state.RoundSteps[i].ToolCalls = slices.DeleteFunc(step.ToolCalls, func(call types.ToolCall) bool {
						return slices.Contains([]string{"structured_analyze_table", "structured_inspect_source", "structured_query_series"}, call.Name)
					})
				}
				for _, call := range step.ToolCalls {
					if call.Name == agenttools.ToolReadDocument && call.Result != nil {
						if test.side == "workbook" {
							delete(call.Result.Data, "image_descriptions")
						}
						if test.wrongPage {
							for _, image := range sourceNavigationRows(call.Result.Data["image_evidence"]) {
								image["page"] = 8
							}
						}
					}
				}
			}
			if test.approved {
				e.turnReview.Last = candidateDecision{Candidate: test.final, CandidateHash: candidateTextDigest(test.final),
					Disposition: candidateAccepted, StandardReview: true,
					TaskHash:     e.candidateTaskDigest(e.turnReview.Query, e.turnReview.Conversation),
					EvidenceHash: reviewDigest(e.sourceConsistencyMessages(state, e.turnReview.Conversation))}
				selection := e.selectFinalReadPublication(ctx, state, test.final, terminalNoFailure, test.outcome)
				require.True(t, selection.fromEvidence)
				require.Contains(t, selection.answer, "高于")
			}
			stream := &terminalAnswerRecorder{}
			stream.attach(e.eventBus)
			var complete event.AgentCompleteData
			e.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
				complete = evt.Data.(event.AgentCompleteData)
				return nil
			})
			e.commitTerminalResult(ctx, state, "run", test.final, "", test.outcome, false, test.failure)
			e.emitCompletionEvent(ctx, state, "run", "turn", time.Now())
			closed, visible := stream.snapshot()
			require.Equal(t, 1, closed)
			require.Equal(t, visible, complete.FinalAnswer)
			require.Equal(t, visible, state.FinalAnswer)
			require.NotRegexp(t, `</?(?:kb|evidence)`, visible)
			switch {
			case !test.enabled:
				require.Equal(t, trace.Final, visible)
			case test.wrongPage:
				require.NotContains(t, visible, "331.2")
				require.NotContains(t, visible, "高于")
			case test.side == "report":
				require.Contains(t, visible, "331.2")
				require.Contains(t, visible, "第 6 页")
				require.NotContains(t, visible, "298.93")
				require.NotContains(t, visible, "高于")
			case test.side == "workbook":
				require.Contains(t, visible, "298.93")
				require.Contains(t, visible, "G87")
				require.NotContains(t, visible, "331.2")
				require.NotContains(t, visible, "高于")
			default:
				for _, fact := range []string{"331.2", "298.93", "32.27", "第 6 页", "G87", "高于"} {
					require.Contains(t, visible, fact)
				}
				require.NotContains(t, visible, "尚缺")
				require.Nil(t, complete.Incomplete)
			}
		})
	}
}

func TestQA4Af5PreservesRecordedComparisonAnswers(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, i := range []int{1, 2} {
			t.Run(fmt.Sprintf("%t/Af4-%d", enabled, i+1), func(t *testing.T) {
				configureAf5Publication(t, enabled)
				trace := loadAf5ComparisonReplays(t)[i]
				e, state, ctx := newAf5ComparisonEngine(t, trace, i == 2)
				// A restored checkpoint has the already approved contents
				// and immutable publication receipt from the preceding run.
				state.IsComplete, state.FinalAnswer = true, trace.Final
				e.turnReview.Committed = true
				e.turnReview.TerminalReceipt.answerHash = candidateTextDigest(trace.Final)
				e.turnReview.TerminalReceipt.evidence = e.terminalEvidenceRevision(ctx, state)
				e.emitCompletionEvent(ctx, state, "run", "turn", time.Now())
				require.Equal(t, trace.Final, state.FinalAnswer)
				require.Contains(t, state.FinalAnswer, "331.2")
				require.Contains(t, state.FinalAnswer, "298.93")
			})
		}
	}
}

func TestFinalReadPreservesCurrentSemanticallyReviewedComparison(t *testing.T) {
	configureAf5Publication(t, true)
	e, state, ctx := newAf5ComparisonEngine(t, loadAf5ComparisonReplays(t)[0], false)
	draft := "两份材料显示的工商业期末库存不同。\n\n棉花周报20240331.pptx 第 6 页在 2023-10 给出 331.2。" +
		"\n\n棉花数据库.xlsx 在 2023-10-31 的 G87 为 298.93。"
	e.turnReview.Last = candidateDecision{Candidate: draft, CandidateHash: candidateTextDigest(draft),
		Disposition: candidateAccepted, StructuredComparisonReview: true, StructuredResultReviewStatus: "kept"}
	selected := e.selectFinalReadPublication(ctx, state, draft, terminalNoFailure, "answer")
	require.False(t, selected.fromEvidence)
}

// Af3-2 KB-16 published an actual latest observation followed by the old
// coverage generator's open-item paragraph. Reuse the recorded plan and
// native return values so nothing in the test supplies a live-stage answer.
func TestFinalReadRemovesRecordedGeneratedGapAfterReading(t *testing.T) {
	for _, test := range []struct {
		name               string
		enabled, goodDraft bool
	}{
		{name: "Af5 removes the old paragraph", enabled: true},
		{name: "Af4 keeps the released bytes"},
		{name: "Af5 keeps the source's scope conclusion", enabled: true, goodDraft: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			configureAf5Publication(t, test.enabled)
			raw, err := os.ReadFile("testdata/qa4/replay/af5-af3-kb16-r2.json.gz")
			require.NoError(t, err)
			file, err := gzip.NewReader(bytes.NewReader(raw))
			require.NoError(t, err)
			defer file.Close()
			decoder := json.NewDecoder(file)
			decoder.UseNumber()
			var archive struct {
				af5ComparisonReplay
				Plan *taskEvidencePlan
			}
			require.NoError(t, decoder.Decode(&archive))
			e, state, ctx := newAf5ComparisonEngine(t, archive.af5ComparisonReplay, false)
			e.taskPlan = archive.Plan
			e.config.TaskPlanningEnabled = false
			e.chatModel = &qa4Stage2CModel{t: t}
			messages := []chat.Message{{Role: "system", Content: "Deliver the recorded authorized observations."}, {Role: "user", Content: archive.Question}}
			e.reviewConversation, e.reviewConversationReady = conversationThroughLastHuman(messages), true
			e.turnReview.Conversation = e.reviewConversation
			e.turnReview.TaskConversation = e.reviewConversation
			e.turnReview.TaskConversationCaptured = true
			steps := state.RoundSteps
			state.RoundSteps = nil
			for _, step := range steps {
				state.CurrentRound = step.Iteration
				if len(step.ToolCalls) == 0 && step.Thought != "" {
					e.checkCandidate(ctx, archive.Question, step.Thought, "", state, messages, false)
				}
				state.RoundSteps = append(state.RoundSteps, step)
				messages = e.appendToolResults(messages, step)
				e.refreshEvidenceCompletion(ctx, archive.Question, state, messages)
			}
			reading, gap, ok := strings.Cut(archive.Final, "\n\n")
			require.True(t, ok)
			require.Contains(t, reading, "17110.0")
			require.Contains(t, reading, "2023-09-22")
			require.Contains(t, reading, "B4705")
			require.Contains(t, gap, "2024-03-01 – 2024-03-31")
			draft, want := archive.Final, archive.Final
			if test.goodDraft {
				draft = reading + "\n\n已读工作表的记录止于上述日期，未覆盖 2024 年 3 月。"
				want = draft
			} else if test.enabled {
				want = reading
			}
			decision := e.checkCandidate(ctx, archive.Question, draft, "", state, messages, false)
			require.Equal(t, candidateAccepted, decision.Disposition, decision.ReasonCode)
			answer, complete := publishAf5StructuredTurn(t, ctx, e, state, func() {
				e.commitTerminalResult(ctx, state, "recorded", decision.Candidate, "", "answer", false)
			})
			require.Equal(t, want, answer)
			require.Equal(t, want, complete.FinalAnswer)
		})
	}
}

func TestFinalReadPublicationSharesTerminalOutlets(t *testing.T) {
	configureAf5Publication(t, true)
	for _, failure := range []terminalFailure{terminalEmpty, terminalReview, terminalNumeric, terminalIncomplete,
		terminalStalled, terminalTruncated, terminalBudget, terminalInterrupted, terminalTableEvidence,
		terminalEndpointEvidence, terminalAnswerEvidence, terminalSourceStalled, terminalCollaborationContention} {
		t.Run(string(failure), func(t *testing.T) {
			e := newTestEngine(t, &mockChat{})
			e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
			e.toolRegistry = qa4ReplayRegistry()
			e.turnReview.Query = "What reading was observed?"
			e.turnReview.DeferTerminalClose = true
			call := terminalLatestSeriesCall()
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
			stream := &terminalAnswerRecorder{}
			stream.attach(e.eventBus)
			var complete event.AgentCompleteData
			e.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
				complete = evt.Data.(event.AgentCompleteData)
				return nil
			})
			e.commitTerminalResult(t.Context(), state, "run", "", "", "failed", false, failure)
			require.Empty(t, stream.contents)
			e.emitCompletionEvent(t.Context(), state, "run", "turn", time.Now())
			closed, content := stream.snapshot()
			require.Equal(t, 1, closed)
			require.Equal(t, []string{content}, stream.contents)
			require.Equal(t, content, complete.FinalAnswer)
			require.Equal(t, content, state.FinalAnswer)
			require.Contains(t, content, "47.25")
			require.Contains(t, content, "Equipment.xlsx")
			require.NotRegexp(t, `</?(?:kb|evidence)`, content)
			require.Nil(t, complete.Incomplete)
		})
	}
}

func TestFinalReadNoValueUsesActualAuthorizedAttempts(t *testing.T) {
	configureAf5Publication(t, true)
	e := newTestEngine(t, &mockChat{})
	e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
	e.toolRegistry = qa4ReplayRegistry()
	e.turnReview.Query = "Which pressure reading was recorded?"
	ctx := context.WithValue(t.Context(), types.LanguageContextKey, "en-US")
	call := terminalLatestSeriesCall()
	call.Result = &types.ToolResult{Error: "private failure details", PreflightRejected: true}
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	var complete event.AgentCompleteData
	e.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
		complete = evt.Data.(event.AgentCompleteData)
		return nil
	})
	e.commitTerminalResult(ctx, state, "run", incompleteAnswerFallback, "", "failed", false)
	e.emitCompletionEvent(ctx, state, "run", "turn", time.Now())
	require.True(t, complete.HasPartialAnswer())
	require.Contains(t, complete.FinalAnswer, "Data query stopped before execution")
	require.Contains(t, complete.FinalAnswer, "supplied no reading for the requested scope")
	require.NotContains(t, complete.FinalAnswer, "private failure details")
	require.NotContains(t, complete.FinalAnswer, "Equipment.xlsx")
	require.NotContains(t, complete.FinalAnswer, "47.25")
	require.NotContains(t, complete.FinalAnswer, incompleteAnswerFallback)

	// A status-shaped answer can have ordinary outcome metadata. The
	// required source read, rather than any phrase, triggers this fallback.
	e.taskPlan = &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{{StructuredTaskGoal: &StructuredTaskGoal{
		SourceMode: "open", SourceTime: "observed_period", Metric: "Pressure", Required: []string{"scoped_value"},
		Period: map[string]any{"grain": "day", "from": "2021-06-03", "to": "2021-06-03"},
	}}}}
	const statusDraft = "A matching reading is still needed."
	selected := e.selectFinalReadPublication(ctx, state, statusDraft, terminalNoFailure, "answer")
	require.True(t, selected.fromEvidence)
	require.True(t, selected.partial)
	require.NotContains(t, selected.answer, statusDraft)
	require.Contains(t, selected.answer, "Data query stopped before execution")
}
