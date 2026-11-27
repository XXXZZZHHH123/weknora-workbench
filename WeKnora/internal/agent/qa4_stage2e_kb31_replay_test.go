package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// Stages 2, 2b and 2c are already covered by the s2-1, s2-2 and s2-3
// fixtures. The stage 2d run regressed after a different native selection:
// SQL bounded two fields but the last-value query selected AF alone.
func TestQA4Stage2EKB31HistoricalFallback(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			r := newQA4Stage2CReplay(t, "s2-4-kb31", enabled, false)
			runtime, binding := engineCollaboration(t)
			owned, err := runtime.AcquireParent(r.ctx, binding.Scope)
			require.NoError(t, err)
			r.engine.SetCollaboration(owned)
			// The log retains the recover disposition, but not its provider
			// payload. The four following assistant drafts are the saved texts.
			r.model.discovery = discoveryReply("recover", "核对本轮已授权原表以及所问范围缺值后可交付的最近记录。")
			require.Nil(t, r.engine.taskPlan, "the historical planner output failed validation")
			require.Equal(t, incompleteAnswerFallback, r.trace.HistoricalFinal)
			require.Equal(t, 34, r.trace.AnswerEvent)
			require.Equal(t, 36, r.trace.CompleteEvent)
			r.through(24)
			qa4Stage2EAssertHistoricalKB31(r)

			var complete event.AgentCompleteData
			r.engine.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
				complete = evt.Data.(event.AgentCompleteData)
				return nil
			})
			for _, draft := range r.trace.Drafts {
				if !r.state.IsComplete {
					r.run(chatAnswer(draft))
				}
			}
			if enabled {
				require.True(t, r.state.IsComplete, "the original four drafted answers ran through the existing exhaustion outlet")
			} else {
				r.exhaust()
			}
			r.engine.emitCompletionEvent(r.ctx, r.state, "s2c", "turn", time.Now())
			answer := r.answer()
			require.False(t, terminalCandidateFallback(answer))
			require.Equal(t, answer, complete.FinalAnswer)
			for _, item := range []string{"棉花数据库.xlsx", "郑棉仓单", "仓单数量:一号棉花", "仓单和有效预报:合计", "20.784", "万吨", "AF3340"} {
				require.Contains(t, answer, item)
			}
			require.Regexp(t, `2024-03-10|2024\s*年\s*3\s*月\s*10\s*日`, answer)
			require.Regexp(t, `2023-09-21|2023\s*年\s*9\s*月\s*21\s*日`, answer)
			require.NotContains(t, answer, "5160", "the historical execution never read this value")
			require.NotContains(t, answer, "60.6", "the historical execution did not read a report")
			t.Logf("Published answer: %s", answer)
		})
	}
}

func qa4Stage2EAssertHistoricalKB31(r *qa4Stage2CReplay) {
	t := r.t
	t.Helper()
	require.Empty(t, r.state.KnowledgeRefs, "historical KB31 did not read any document")
	analysis := r.results[14]
	require.Equal(t, "2024-03-10", analysis.ExecutionArgs()["start_date"])
	require.Equal(t, "2024-03-10", analysis.ExecutionArgs()["end_date"])
	require.Equal(t, true, analysis.Result.Data["input_complete"])
	require.Empty(t, analysis.Result.Data["rows"])
	for _, field := range []string{"warehouse.B", "warehouse.AF"} {
		input := coverageObject(coverageObject(analysis.Result.Data["inputs"])[field])
		require.Contains(t, input, "valid_count")
		require.Zero(t, citationInt(input["valid_count"]), field)
	}
	raw := r.results[24]
	require.Equal(t, "warehouse.AF", raw.ExecutionArgs()["series_id"])
	rows := sourceNavigationRows(raw.Result.Data["rows"])
	require.Len(t, rows, 1)
	require.Equal(t, "20.784", rows[0]["raw_value"])
	require.Equal(t, "AF3340", rows[0]["source_cell"])
	require.Equal(t, "A3340", rows[0]["date_cell"])
	require.Equal(t, "万吨", coverageObject(raw.Result.Data["series"])["unit"])
}

func TestQA4Stage2EKB31AuthorizedRecovery(t *testing.T) {
	// The historical variant supplies a filename as the version reason.
	// The current path requires an explicit user selection or an actual body.
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "false")
	for _, mode := range []string{"normal", "rewrite", "ignored", "budget"} {
		t.Run(mode, func(t *testing.T) { qa4Stage2EKB31AuthorizedRecovery(t, mode) })
	}
}

func qa4Stage2EKB31AuthorizedRecovery(t *testing.T, mode string) {
	r := newQA4Stage2CReplay(t, "s2-4-kb31", true, true)
	r.through(24)
	qa4Stage2EAssertHistoricalKB31(r)

	// These model selections and native reads are the corrected execution,
	// separate from the five historical receipts. The B response was produced
	// by a read of the identical offline dataset snapshot. Both report pages
	// come from the actual authorized stored slide text and its locators.
	goal := map[string]any{
		"goal_index": 0, "request_basis": r.trace.Question, "source_mode": "named_first",
		"primary_ref": "棉花数据库", "metric": "仓单数量:一号棉花", "source_time": "observed_period", "version_task": "adopt_if_conflict",
		"period":          map[string]any{"from": "2024-03-10", "to": "2024-03-10", "grain": "day"},
		"required":        []string{"scoped_value"},
		"on_verified_gap": []string{"report_gap", "last_valid_same_field", "attributed_supplement"},
		"delivery":        map[string]any{"kind": "text", "aggregation": "as_reported"},
	}
	r.engine.refreshEvidenceCompletion(r.ctx, r.trace.Question, r.state, r.messages)
	var ref string
	for key, item := range r.engine.evidenceCompletion.refs {
		if item.Kind == "directory" && item.SeriesID == "warehouse.B" {
			ref = key
			break
		}
	}
	require.NotEmpty(t, ref)
	read := r.trace.Calls[5]
	read.Arguments = maps.Clone(read.Arguments)
	read.Arguments[coverageArgument] = map[string]any{
		"goal_index": 0, "goal": goal, "role": "primary", "selection_ref": ref,
		"period_from": "2024-03-10", "period_to": "2024-03-10", "grain": "day",
	}
	r.calls([]qa4Stage2Call{read})
	require.Equal(t, "no_data", r.results[101].Result.Data["status"])

	// Even with no useful draft, the existing reserved read must look up the
	// last valid day of the selected field before supplementing it.
	r.run(chatAnswer(""))
	var last types.ToolCall
	for _, call := range r.state.RoundSteps[len(r.state.RoundSteps)-1].ToolCalls {
		if strings.HasSuffix(call.ID, "-coverage-read") && call.ExecutionArgs()["series_id"] == "warehouse.B" {
			last = call
			break
		}
	}
	require.NotNil(t, last.Result, "the product must spend the reserved read on B")
	require.True(t, last.Result.Success, last.Result.Error)
	require.Equal(t, "2023-09-21", last.ExecutionArgs()["start_date"])
	require.Equal(t, "2023-09-21", last.ExecutionArgs()["end_date"])
	b := sourceNavigationRows(last.Result.Data["rows"])
	require.Len(t, b, 1)
	require.Equal(t, "5160", b[0]["raw_value"])
	require.Equal(t, "B3340", b[0]["source_cell"])
	require.Equal(t, coverageObject(r.results[24].Result.Data["source"])["sha256"], coverageObject(last.Result.Data["source"])["sha256"])

	conflict := r.trace.Chunks[0]
	r.index.additional = []*types.SearchResult{{
		ID: conflict.ID, KnowledgeID: conflict.KnowledgeID, KnowledgeBaseID: qa4Stage2Library,
		KnowledgeTitle: r.trace.Documents[0].Title, Content: conflict.Content,
		ChunkIndex: conflict.ChunkIndex, SourceLocators: conflict.SourceLocators, Score: .99,
	}}
	r.run(chatAnswer(""))
	require.False(t, r.state.IsComplete, "the workbench should still allow authorized supplemental evidence")
	require.Len(t, r.index.searches, 1)
	require.Equal(t, "仓单数量:一号棉花 2024-03-10", r.index.searches[0].QueryText)
	gap, open := r.engine.evidenceCompletionGap()
	require.True(t, open)
	require.Equal(t, "read", gap.NextRead)
	sources := make([]any, 0, len(r.trace.Documents))
	for _, report := range r.trace.Documents {
		sources = append(sources, map[string]any{
			"knowledge_id": report.ID, "expected_file_name": report.FileName, "page": 18,
			"date_evidence": map[string]any{"date": "2024-03-10", "source_field": "file_name", "source_text": "20240310"},
		})
	}
	var reads []qa4Stage2Call
	for idx, report := range r.trace.Documents {
		var navigation string
		for ref, item := range r.engine.evidenceCompletion.refs {
			if item.Kind == "navigation" && item.SourceID == report.ID && item.Page == 18 {
				navigation = ref
				break
			}
		}
		require.NotEmpty(t, navigation, "the actual search found this physical page")
		reads = append(reads, qa4Stage2Call{Event: 104 + idx, Tool: agenttools.ToolReadDocument, Arguments: map[string]any{
			"id": report.ID, "page": 18,
			"source_scope":   map[string]any{"selection_reason": "核对所问期间仓单的授权周报及其披露口径，核对原报告与标为冲突测试版的区别", "sources": sources},
			coverageArgument: map[string]any{"goal_index": 0, "role": "supplement", "gap_id": gap.GapID, "selection_ref": navigation, "page": 18},
		}})
	}
	r.calls(reads)
	r.engine.refreshEvidenceCompletion(r.ctx, r.trace.Question, r.state, r.messages)
	values := []string{"62.3", "60.6"}
	var declarations []map[string]any
	var citations []string
	for idx, value := range values {
		receipt := r.results[104+idx].Result
		require.True(t, receipt.Success, receipt.Error)
		require.True(t, agenttools.IsCurrentDocumentRead(r.engine.toolRegistry, receipt))
		require.Contains(t, receipt.Output, "郑棉仓单：合计"+value+"万吨")
		require.Equal(t, 18, citationInt(receipt.Data["page"]))
		chunk := r.trace.Chunks[idx]
		cite := r.engine.modelContext.CitableChunkHandle(chunk.ID)
		require.NotEmpty(t, cite)
		citations = append(citations, fmt.Sprintf(`<ref id="%s"/>`, cite))
		var selected string
		for ref, fact := range r.engine.evidenceCompletion.refs {
			if fact.ChunkID == chunk.ID && fact.Kind == "body" {
				selected = ref
				break
			}
		}
		require.NotEmpty(t, selected, "actual returned report text has an evidence reference")
		declarations = append(declarations, map[string]any{
			"goal_index": 0, "role": "supplement", "selection_ref": selected, "page": 18,
			"span": "郑棉仓单：合计" + value + "万吨", "value_text": value, "unit": "万吨",
			"metric": "郑棉仓单",
		})
	}
	declarations[1]["version_basis_ref"] = declarations[0]["selection_ref"]
	declarations[1]["version_basis"] = "冲突测试版"
	declaration, err := json.Marshal(declarations)
	require.NoError(t, err)
	prose := "《棉花数据库.xlsx》的「郑棉仓单」表中，仓单数量:一号棉花（S0049502）在2024-03-10没有有效观测值。该字段最新原值为5160，日期是2023-09-21，日期格A3340，取值格B3340，单位未确认。\n\n" +
		"2024-03-10《棉花周报20240310.pptx》第18页的周度披露是“郑棉仓单：合计60.6万吨”" + citations[1] +
		"。同日《棉花周报20240310-冲突测试版.pptx》第18页写的是“郑棉仓单：合计62.3万吨”" + citations[0] +
		"。数据库没有当天的日度记录。"
	// A model must author the version decision. The fixture keeps it apart
	// from the saved historical answer and from the native report receipts.
	const choice = "补充披露暂采用《棉花周报20240310.pptx》；另一份《棉花周报20240310-冲突测试版.pptx》明确标有“冲突测试版”，两者都保留各自出处。"
	full := prose + "\n\n" + choice
	draft := full + coverageAnswerTag + string(declaration) + coverageAnswerClose
	switch mode {
	case "budget":
		initial := r.engine.acceptCoverageAnswer(r.ctx, r.trace.Question, r.state, r.messages, draft, true)
		checked := r.engine.checkCandidate(r.ctx, r.trace.Question, r.engine.modelContext.DecodeOutputText(initial), "", r.state, r.messages, false)
		require.NoError(t, checked.Err)
		require.Empty(t, checked.Unsupported)
		require.Equal(t, r.engine.modelContext.DecodeOutputText(full), r.engine.turnReview.CoverageDraft.Candidate)
		r.engine.turnReview.Repairs = maxTurnCandidateRepairs
		r.exhaust()
	case "rewrite", "ignored":
		r.engine.config.StructuredResultReviewEnabled = false
		r.engine.acceptCoverageAnswer(r.ctx, r.trace.Question, r.state, r.messages, draft, true)
		review := r.engine.currentCoverageDraftReview(r.ctx, r.state)
		require.NotEmpty(t, review.version.SelectedSource, "native comparable version verification")
		r.run(chatAnswer(prose + coverageAnswerTag + string(declaration) + coverageAnswerClose))
		require.False(t, r.state.IsComplete)
		require.True(t, r.engine.turnReview.CoverageFeedbackSent, "the original revision receives the verified version choice")
		var feedback string
		for _, message := range r.messages {
			feedback += message.Content
		}
		require.Contains(t, feedback, "checked_provisional_version_choice")
		require.Contains(t, feedback, "basis_quote")
		if mode == "ignored" {
			paid := r.engine.turnReview.Repairs
			r.run(chatAnswer(prose))
			require.Equal(t, paid, r.engine.turnReview.Repairs, "a second reply cannot consume another revision for the same omission")
			require.Equal(t, r.engine.modelContext.DecodeOutputText(prose), r.answer())
			require.NotContains(t, r.state.FinalAnswer, choice)
			return
		}
		r.run(chatAnswer(full))
		require.True(t, r.state.IsComplete, "the original revision supplied the complete answer")
	default:
		r.run(chatAnswer(draft))
		require.True(t, r.state.IsComplete, "ordinary execution should complete with both checked report versions: %+v", r.engine.turnReview.Last.CoverageFeedback)
	}
	require.Equal(t, r.engine.modelContext.DecodeOutputText(full), r.state.FinalAnswer)

	answer := r.answer()
	t.Logf("Published answer: %s", answer)
	for _, item := range []string{"2024-03-10", "2023-09-21", "5160", "B3340", "单位未确认", "棉花周报20240310", "第18页", "60.6万吨", "62.3万吨",
		"补充披露暂采用《棉花周报20240310.pptx》", "《棉花周报20240310-冲突测试版.pptx》明确标有“冲突测试版”"} {
		require.Contains(t, answer, item)
	}
	require.Equal(t, 1, strings.Count(answer, "没有有效"))
	require.Equal(t, 1, strings.Count(answer, "5160"))
	require.Equal(t, 1, strings.Count(answer, "60.6万吨"))
	require.Equal(t, 1, strings.Count(answer, "62.3万吨"))
	require.Less(t, strings.Index(answer, "5160"), strings.Index(answer, "60.6万吨"))
}
