package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestRelaxedTerminalEvidenceSwitch(t *testing.T) {
	for _, test := range []struct {
		name, publication, relaxed string
		want                       bool
	}{
		{name: "default", want: true},
		{name: "explicit enabled", publication: "true", relaxed: "true", want: true},
		{name: "explicit strict", publication: "true", relaxed: "false"},
		{name: "legacy publication", publication: "false", relaxed: "true"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", test.publication)
			t.Setenv("WEKNORA_AGENT_RELAXED_TERMINAL_EVIDENCE", test.relaxed)
			require.Equal(t, test.want, relaxedTerminalEvidenceEnabled())
		})
	}
}

// Use the same report and worksheet receipts as the Af3 comparison replay.
// A model can name their independently verified readings without repeating
// exact citation IDs. The normal terminal outlet and completion must agree.
func TestQA4Af4ComparisonUsesCurrentReadingsWithoutExactDraftCitations(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_FINAL_READ_ANSWER", "false")
	const draft = "两处读数不同。\n\n" +
		"棉花周报20240331.pptx 第 6 页的中国棉花平衡表中，2023 年 10 月工商业期末库存是 331.2。\n\n" +
		"棉花数据库.xlsx 中国棉花平衡表的 2023-10-31 工商业期末库存为 298.93，位于 G87。"
	for _, test := range []struct {
		name, flag, candidate string
		earlierDraft, reverse bool
		periodMismatch        bool
		neighborTable         bool
		reportOnly            bool
		wantComparison        bool
	}{
		{name: "default, missing citations", candidate: draft, wantComparison: true},
		{name: "explicit strict", flag: "false", candidate: draft},
		{name: "exhausted after a readable draft", earlierDraft: true, reverse: true, wantComparison: true},
		{name: "different period", candidate: draft, periodMismatch: true},
		{name: "neighboring table", candidate: draft, neighborTable: true},
		{name: "report only", reportOnly: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "true")
			t.Setenv("WEKNORA_AGENT_RELAXED_TERMINAL_EVIDENCE", test.flag)
			trace := loadAf3KB28Replay(t, 1)
			if test.reportOnly {
				for i := range trace.Steps {
					trace.Steps[i].ToolCalls = slices.DeleteFunc(trace.Steps[i].ToolCalls, func(call types.ToolCall) bool {
						return call.Name == "structured_query_series" || call.Name == "structured_analyze_table"
					})
				}
			}
			if test.reverse {
				slices.Reverse(trace.Steps)
			}
			engine, state, ctx, messages := newAf3KB28ReplayEngine(t, trace, &mockChat{})
			binder, err := engine.toolRegistry.GetTool(agenttools.ToolBindDocumentScope)
			require.NoError(t, err)
			args, err := json.Marshal(agenttools.DocumentScopePlan{Reason: "Use the original report selected by the recorded read.",
				Sources: []agenttools.DocumentSourceSelection{{KnowledgeID: trace.Document.ID, FileName: trace.Document.FileName, Title: trace.Document.Title}}})
			require.NoError(t, err)
			binding, err := binder.Execute(ctx, args)
			require.NoError(t, err)
			require.True(t, binding.Success, binding.Error)
			state.RoundSteps = append([]types.AgentStep{{ToolCalls: []types.ToolCall{{
				ID: "replay-document-binding", Name: agenttools.ToolBindDocumentScope, Result: binding,
			}}}}, state.RoundSteps...)
			target := engine.taskPlan.CoverageTargets[0]
			if !test.reportOnly {
				target.Metric = "中国棉花平衡表期末库存"
			}
			if test.periodMismatch {
				target.Period = map[string]any{"grain": "month", "from": "2023-11-01", "to": "2023-11-30"}
			}
			if test.neighborTable {
				target.Table = "同一页上的相邻表"
			}
			if test.earlierDraft {
				state.RoundSteps = append(state.RoundSteps, types.AgentStep{Thought: draft})
			}
			candidate := engine.checkCandidate(ctx, trace.Question, test.candidate, "", state, messages, false)
			var complete event.AgentCompleteData
			engine.eventBus.On(event.EventAgentComplete, func(_ context.Context, item event.Event) error {
				complete = item.Data.(event.AgentCompleteData)
				return nil
			})
			engine.commitTerminalResult(ctx, state, "run", candidate.Candidate, "", "failed", false, terminalTableEvidence)
			engine.emitCompletionEvent(ctx, state, "run", "turn", time.Now())
			require.Equal(t, state.FinalAnswer, complete.FinalAnswer)
			require.NotContains(t, complete.FinalAnswer, "<kb")
			require.NotContains(t, complete.FinalAnswer, "<evidence")
			if test.wantComparison {
				require.True(t, strings.HasPrefix(complete.FinalAnswer, "两处来源给出的数字不同"))
				for _, fact := range []string{"331.2", "298.93", "32.27", "第 6 页", "G87"} {
					require.Contains(t, complete.FinalAnswer, fact)
				}
				require.Nil(t, complete.Incomplete)
				require.Equal(t, terminalNoFailure, engine.turnReview.TerminalReceipt.failure)
			} else {
				require.NotContains(t, complete.FinalAnswer, "高于")
				if test.reportOnly {
					require.Contains(t, complete.FinalAnswer, "331.2")
					require.Contains(t, complete.FinalAnswer, "第 6 页")
					require.Contains(t, complete.FinalAnswer, "另一来源")
					require.NotContains(t, complete.FinalAnswer, "298.93")
				}
				if test.periodMismatch || test.neighborTable {
					require.NotContains(t, complete.FinalAnswer, "331.2")
					require.NotContains(t, complete.FinalAnswer, "298.93")
				}
			}
		})
	}
}

func TestQA4Af4MetricSubstringDoesNotSelectAReading(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "true")
	t.Setenv("WEKNORA_AGENT_RELAXED_TERMINAL_EVIDENCE", "true")
	trace := loadAf3KB28Replay(t, 1)
	engine, state, ctx, _ := newAf3KB28ReplayEngine(t, trace, &mockChat{})
	target := engine.taskPlan.CoverageTargets[0]
	target.Metric = "期末库存" // Only a substring of the provided native header.
	record, _ := engine.terminalCoverageRecord(ctx, state)
	facts, aligned := engine.tableReceiptReadingsForDelivery(target, record, "", true)
	require.False(t, aligned)
	require.Empty(t, facts, "a text overlap cannot choose one of the requested readings")
}

func TestQA4Af4ReadingsNamedUnderSeparateWorkbookHeading(t *testing.T) {
	reading := coverageEvidence{
		Kind: "cell", SourceDomain: "structured", Operation: "query_series",
		SourceID: "selected", DatasetID: "selected", KnowledgeBaseID: "library", KnowledgeID: "selected-workbook",
		SourceTitle: "Register.xlsx", Sheet: "Operations", SeriesID: "elapsed", Column: "B", Metric: "Elapsed time",
		Date: "2021-06-03", DateCell: "A15", Cell: "B15", Raw: json.Number("47.25"),
	}
	other := reading
	other.SeriesID, other.Column, other.Metric, other.Cell, other.Raw = "count", "C", "Count", "C15", json.Number("12")
	candidate := "**Register.xlsx, Operations**\n\nElapsed time on 2021-06-03 was 47.25 at B15."
	for _, mode := range []string{"strict terminal", "separate heading", "ambiguous identical names"} {
		t.Run(mode, func(t *testing.T) {
			facts := []coverageEvidence{reading, other}
			if mode == "ambiguous identical names" {
				duplicate := reading
				duplicate.SourceID, duplicate.DatasetID, duplicate.KnowledgeID = "different", "different", "different-workbook"
				facts = append(facts, duplicate)
			}
			delivery := terminalCoverageDelivery{relaxed: mode != "strict terminal", record: coverageRecord{Evidence: facts}}
			for _, item := range facts {
				delivery.facts = append(delivery.facts, terminalCoverageFact{coverageFact: coverageFact{Role: "scoped_value", Evidence: item}})
			}
			delivery.retainSelectedUnplannedReadings(nil, candidate)
			if mode == "separate heading" {
				require.Len(t, delivery.facts, 1)
				require.Equal(t, reading, delivery.facts[0].Evidence)
			} else {
				require.Empty(t, delivery.facts)
			}
		})
	}
}
