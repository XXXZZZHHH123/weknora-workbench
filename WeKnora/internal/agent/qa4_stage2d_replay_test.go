package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/stretchr/testify/require"
)

// The s2-3 fixtures use original SSE result ordinals. SQLite agent_steps
// supplies decoded arguments and the published candidate of each turn
// (iterations 12 and 7). The latter predates KB-31's two mechanical lines.
// The persisted evidence manifests supply KB-16's four selectors.
// Authorized report excerpts come from the cited chunks in the local source
// snapshot. IDs and source hashes are aliased, then typed result hashes are
// recomputed. Native registration, validation and publishing run in process.
// Both historical task plans were rejected. No replacement plan or model
// declarations, fresh provider answers, or synthetic values are introduced.
type qa4Stage2DCitationSelector struct {
	Tag        string
	ColumnName string `json:"column_name"`
	RowIndex   int    `json:"row_index"`
}

func TestQA4Stage2DHistoricalReplay(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fixture   string
		lastEvent int
		forced    bool
		omitValue bool
	}{
		{"KB16_default", "s2-3-kb16", 49, false, false},
		{"KB16_forced", "s2-3-kb16", 49, true, false},
		{"KB31_default", "s2-3-kb31", 34, false, false},
		{"KB31_forced", "s2-3-kb31", 34, true, false},
		{"KB31_draft_missing_primary_value", "s2-3-kb31", 34, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newQA4Stage2CReplay(t, tc.fixture, tc.forced, false)
			if !tc.forced {
				// The stage 2c constructor has registered the original env for cleanup.
				require.NoError(t, os.Unsetenv("WEKNORA_AGENT_EVIDENCE_COMPLETION"))
			}
			require.Equal(t, tc.forced, r.engine.evidenceCompletionEnabled())
			require.Nil(t, r.engine.taskPlan)
			structuredReads := qa4Stage2DCountReads()
			// Omit superseded drafts and navigation unrelated to these final
			// citations. Keep the recorded round positions of the used calls.
			var expectedReads int32
			for _, call := range r.trace.Calls {
				if len(call.Response) > 0 {
					expectedReads++
				}
				r.state.CurrentRound = call.Iteration
				r.calls([]qa4Stage2Call{call})
				if call.HistoricalError != "" {
					result := r.results[call.Event].Result
					require.False(t, result.Success)
					require.Equal(t, call.HistoricalError, result.Error)
				}
			}
			read := r.results[tc.lastEvent].Result
			require.True(t, read.Success, read.Error)
			require.True(t, agenttools.IsCurrentDocumentRead(r.engine.toolRegistry, read))
			draft := qa4Stage2DRemintCitations(t, r, tc.fixture)
			if tc.omitValue {
				// Keep the original gap and its source/field anchor. Remove just
				// the main evidence sentence, while leaving the report intact.
				const recent = "的仓单数据只到 2023 年 9 月：最后一条有数值的记录为 2023年9月21日，「仓单数量:一号棉花」为 5160（该字段单位在字段目录中尚未确认），原单元格为郑棉仓单!B3340。"
				require.Contains(t, draft, recent)
				draft = strings.Replace(draft, recent, "的「仓单数量:一号棉花」没有当日记录。", 1)
				require.NotContains(t, draft, "5160")
			}

			r.run(chatAnswer(draft))
			r.exhaust()
			answer := r.answer()
			t.Logf("Published answer:\n%s", answer)
			for _, forbidden := range []string{"query_unavailable", "needs_clarification", "primary_unverified", "last_valid_same_field", "<structured_evidence", "<query_scope>", "取数依据未通过", "请告诉我", "部分来源请求在读取开始前被拦下", "在已核查范围 2024-03-10 内没有有效观测值", "已核查范围内的最近有效值 5160"} {
				require.NotContains(t, answer, forbidden)
			}
			require.Equal(t, expectedReads, structuredReads.Load(), "replay only issued the recorded calls")

			var complete event.AgentCompleteData
			r.engine.eventBus.On(event.EventAgentComplete, func(_ context.Context, item event.Event) error {
				complete = item.Data.(event.AgentCompleteData)
				return nil
			})
			r.engine.emitCompletionEvent(r.ctx, r.state, "s2c", "turn", time.Now())
			require.Equal(t, answer, complete.FinalAnswer)
			var knowledgeIDs []string
			for _, ref := range r.state.KnowledgeRefs {
				knowledgeIDs = append(knowledgeIDs, ref.ID)
			}
			if tc.lastEvent == 49 {
				qa4Stage2DAssertKB16(t, r, answer)
				require.Equal(t, []string{"replay-0331-slide-2-table"}, knowledgeIDs)
				var savedIDs []string
				for _, ref := range r.state.EvidenceRefs {
					require.Equal(t, candidateTextDigest(answer), ref.CandidateHash)
					savedIDs = append(savedIDs, ref.ID)
				}
				require.Len(t, savedIDs, 4)
				require.ElementsMatch(t, uniqueCandidateEvidenceIDs(answer), savedIDs)
				require.Len(t, complete.EvidenceRefs, len(savedIDs))
				return
			}
			qa4Stage2DAssertKB31(t, r, answer, tc.omitValue)
			require.ElementsMatch(t, []string{"replay-0310-slide-18", "replay-0310-slide-2-table"}, knowledgeIDs)
		})
	}
}

// This variant changes #29 to a single selected field and loads only the
// requested month. Both receipts were generated offline by the native Python
// data store from the same original workbook snapshot. Only identifiers were
// aliased and the typed result hashes updated. The deliberately incomplete
// draft is identical before and after the automatic read.
func TestQA4Stage2DBoundedSQLLatestRecovery(t *testing.T) {
	qa4Stage2DBoundedSQLRecovery(t, "s2-3-kb16-bounded-latest", []qa4Stage2DLatestCell{{"CF.CZC", "17110", "B4705"}})
}

// The batch variant preserves the complete historical binding and uses the
// exact date, SQL and row limit of the native recovery request.
func TestQA4Stage2DBoundedSQLBatchRecovery(t *testing.T) {
	qa4Stage2DBoundedSQLRecovery(t, "s2-3-kb16-bounded-batch", []qa4Stage2DLatestCell{
		{"CF.CZC", "17110", "B4705"}, {"CF01M.CZC", "17110", "C4705"}, {"CF03M.CZC", "16965", "D4705"},
		{"CF05M.CZC", "16865", "E4705"}, {"CF07M.CZC", "16760", "F4705"},
		{"CF09M.CZC", "16690", "G4705"}, {"CF11M.CZC", "17275", "H4705"},
	})
}

type qa4Stage2DLatestCell struct{ contract, value, cell string }

func qa4Stage2DBoundedSQLRecovery(t *testing.T, fixture string, cells []qa4Stage2DLatestCell) {
	t.Helper()
	const lastDay = "2023-09-22"
	for _, mode := range []string{"true", "false", "unset"} {
		t.Run(mode, func(t *testing.T) {
			enabled := mode == "true"
			r := newQA4Stage2CReplay(t, fixture, enabled, false)
			if mode == "unset" {
				require.NoError(t, os.Unsetenv("WEKNORA_AGENT_EVIDENCE_COMPLETION"))
			}
			r.engine.config.StructuredResultReviewEnabled = false
			require.Equal(t, enabled, r.engine.evidenceCompletionEnabled())
			reads := qa4Stage2DCountReads()
			analysis, latest := r.trace.Calls[0], r.trace.Calls[1]
			r.calls([]qa4Stage2Call{analysis})
			require.EqualValues(t, 1, reads.Load())
			args := latest.Arguments
			inputs := coverageObject(r.results[analysis.Event].Result.Data["inputs"])
			require.Len(t, inputs, len(cells))
			for _, cell := range cells {
				input := coverageObject(inputs["futures.close."+cell.contract])
				require.Equal(t, json.Number("0"), input["valid_count"])
				require.Nil(t, input["valid_last_date"], "SQL did not load the published endpoint")
				require.Equal(t, lastDay, coverageObject(input["series"])["last_valid_date"])
			}

			draft := r.trace.Drafts[0]
			expectedAnswer := draft
			require.NotContains(t, draft, "17110")
			modelCalls := r.model.callCount
			r.run(chatAnswer(draft))
			require.Equal(t, modelCalls+2, r.model.callCount, "the draft and legacy discovery review run on both paths")
			require.True(t, isDiscoveryDeliveryReviewFormat(r.model.opts[modelCalls+1].Format))
			if enabled {
				require.False(t, r.state.IsComplete, "allow recovery to finish before publishing")
				require.EqualValues(t, 2, reads.Load())
				step := r.state.RoundSteps[len(r.state.RoundSteps)-1]
				require.Len(t, step.ToolCalls, 1)
				call := step.ToolCalls[0]
				require.Equal(t, latest.Tool, call.ExecutionName())
				require.Equal(t, args, call.ExecutionArgs())
				require.Equal(t, lastDay, call.ExecutionArgs()["start_date"])
				require.Equal(t, lastDay, call.ExecutionArgs()["end_date"])
				require.Equal(t, analysis.Arguments["source_binding"], call.ExecutionArgs()["source_binding"])
				require.Equal(t, r.trace.Question, call.ExecutionArgs()["source_question"])
				require.NotNil(t, call.Result)
				require.True(t, call.Result.Success, call.Result.Error)
				bounds := coverageObject(call.Result.Data["filters"])
				if len(cells) == 1 {
					require.Equal(t, lastDay, call.Result.Data["valid_last_date"])
				} else {
					bounds = coverageObject(call.Result.Data["requested_range"])
					require.EqualValues(t, len(cells), args["limit"])
					rows, ok := call.Result.Data["rows"].([]any)
					require.True(t, ok)
					require.Len(t, rows, len(cells))
					for i, cell := range cells {
						require.Equal(t, []any{"futures.close." + cell.contract, lastDay, json.Number(cell.value + ".0"), cell.cell, "A4705"}, rows[i])
					}
				}
				require.Equal(t, lastDay, bounds["start_date"])
				require.Equal(t, lastDay, bounds["end_date"])
				record := r.engine.evidenceCompletion.record
				for _, cell := range cells {
					require.True(t, slices.ContainsFunc(record.Evidence, func(item coverageEvidence) bool {
						return item.Kind == "last_valid" && item.ToolCallID == call.ID && item.SeriesID == "futures.close."+cell.contract &&
							item.Date == lastDay && item.Cell == cell.cell && (len(cells) > 1 || item.DateCell == "A4705") &&
							coverageCurrentEvidence(record, item)
					}), "the bounded result registered the original cell for %s", cell.contract)
				}
				// Script the model response which uses the verified endpoint
				// from the completed bounded read. The historical draft remains
				// unchanged for the default path where this read never ran.
				var displayed []string
				for _, cell := range cells {
					field := cell.contract
					if len(cells) > 1 {
						field = "futures.close." + field
					}
					displayed = append(displayed, field+" "+cell.value+"（"+cell.cell+"）")
				}
				revised := draft + "\n\n同字段在 " + lastDay + " 的有效值位于日期格 A4705：" + strings.Join(displayed, "，") + "。"
				expectedAnswer = revised
				r.engine.evidenceCompletion.enabled = false
				r.run(chatAnswer(revised))
				require.Equal(t, modelCalls+3, r.model.callCount, "the existing model round authored the revised draft")
				require.Empty(t, r.engine.turnReview.Last.Unsupported, "every displayed number comes from the bounded receipt")
				require.EqualValues(t, 2, reads.Load(), "the same missing field may be read only once")
			} else {
				require.EqualValues(t, 1, reads.Load())
			}
			// Stop after replaying the draft if legacy completeness still asks to
			// collect evidence. The production budget outlet retains checked cells.
			r.exhaust()
			answer := r.answer()
			t.Logf("Published answer:\n%s", answer)
			require.Equal(t, expectedAnswer, answer)
			if enabled {
				for _, cell := range cells {
					require.Contains(t, answer, cell.contract)
					require.Contains(t, answer, cell.value)
					require.Contains(t, answer, cell.cell)
				}
			}
		})
	}
}

func qa4Stage2DCountReads() *atomic.Int32 {
	transport := http.DefaultTransport
	var reads atomic.Int32
	// The replay constructor already registered cleanup of the global transport.
	http.DefaultTransport = qa4Stage2Transport(func(request *http.Request) (*http.Response, error) {
		reads.Add(1)
		return transport.RoundTrip(request)
	})
	return &reads
}

// The historical public IDs depended on a per-turn nonce. Keep every selected
// row/column and document chunk while letting the native replay mint its IDs.
func qa4Stage2DRemintCitations(t *testing.T, r *qa4Stage2CReplay, fixture string) string {
	t.Helper()
	draft := r.trace.Drafts[0]
	for _, chunk := range r.trace.Chunks {
		old := fmt.Sprintf(`<kb doc="%s" chunk_id="%s" kb_id="%s" />`, r.trace.Documents[len(r.trace.Documents)-1].Title, chunk.ID, qa4Stage2Library)
		if strings.Contains(draft, old) {
			handle := r.engine.modelContext.CitableChunkHandle(chunk.ID)
			require.NotEmpty(t, handle)
			draft = strings.ReplaceAll(draft, old, fmt.Sprintf(`<ref id="%s"/>`, handle))
		}
	}
	raw, err := os.ReadFile("testdata/qa4/replay/" + fixture + ".json")
	require.NoError(t, err)
	var trace struct {
		Selectors []qa4Stage2DCitationSelector `json:"citation_selectors"`
	}
	require.NoError(t, json.Unmarshal(raw, &trace))
	for _, selected := range trace.Selectors {
		var handle string
		for id, obs := range r.engine.turnReview.StructuredCitations {
			selector, _ := obs.Directory["selector"].(map[string]any)
			if obs.CallID == r.results[29].ID && selector["column_name"] == selected.ColumnName && citationInt(selector["row_index"]) == selected.RowIndex {
				handle = r.engine.modelContext.CitableEvidenceHandle(id)
				break
			}
		}
		require.NotEmpty(t, handle, "%s row %d", selected.ColumnName, selected.RowIndex)
		old := fmt.Sprintf(`<evidence evidence_id="%s" />`, selected.Tag)
		require.Contains(t, draft, old)
		draft = strings.ReplaceAll(draft, old, fmt.Sprintf(`<ref id="%s"/>`, handle))
	}
	return draft
}

func qa4Stage2DAssertKB16(t *testing.T, r *qa4Stage2CReplay, answer string) {
	t.Helper()
	require.Equal(t, "query_unavailable", r.results[20].Result.Data["status"])
	require.Equal(t, "needs_clarification", r.results[25].Result.Data["status"])
	prior, bound := r.results[25].ExecutionArgs(), r.results[29].ExecutionArgs()
	require.Equal(t, "2024-03-01", prior["start_date"])
	require.Equal(t, "2024-03-31", bound["end_date"])
	require.Equal(t, prior["end_date"], bound["end_date"])
	require.Equal(t, prior["series_ids"], bound["series_ids"])
	inputs := coverageObject(r.results[29].Result.Data["inputs"])
	require.Len(t, inputs, 7)
	for id, raw := range inputs {
		input := coverageObject(raw)
		series := coverageObject(input["series"])
		require.Equal(t, "收盘价", series["metric"])
		contract := strings.TrimPrefix(id, "futures.close.")
		require.Equal(t, contract, coverageObject(series["dimensions"])["contract"])
		coverage := coverageObject(input["observation_coverage"])
		truncated := contract == "CF03M.CZC" || contract == "CF07M.CZC" || contract == "CF11M.CZC"
		require.Equal(t, truncated, coverage["truncated"], contract)
	}
	require.Contains(t, r.trace.HistoricalFinal, "2023-09-22")
	require.NotContains(t, r.trace.HistoricalFinal, "17110")
	require.Equal(t, r.trace.Drafts[0], r.trace.HistoricalFinal)
	for _, want := range []string{"查不到", "2023-09-22", "2024年3月", "棉花周报20240331.pptx", "第2页", "CF2405", "CF2409", "CF2501", "16085", "16265", "16095", "不能直接补作该表", "<kb ", "<evidence "} {
		require.Contains(t, answer, want)
	}
	require.NotContains(t, answer, "17110", "the original draft omitted the endpoint already present in the SQL input")
	require.Equal(t, r.engine.modelContext.DecodeOutputText(qa4Stage2DRemintCitations(t, r, "s2-3-kb16")), answer)
}

func qa4Stage2DAssertKB31(t *testing.T, r *qa4Stage2CReplay, answer string, omitValue bool) {
	t.Helper()
	require.Equal(t, "no_data", r.results[16].Result.Data["status"])
	require.Equal(t, "ok", r.results[21].Result.Data["status"])
	require.True(t, r.results[25].Result.PreflightRejected)
	require.True(t, r.results[30].Result.Success)
	require.Contains(t, r.results[34].Result.Output, "郑棉仓单：合计60.6万吨")
	require.True(t, strings.HasPrefix(r.trace.HistoricalFinal, r.trace.Drafts[0]+"\n\n棉花数据库.xlsx /"))
	for _, want := range []string{"查不到", "2024年3月10日", "棉花周报20240310", "第18页", "60.6万吨", "不含有效预报", "周度值", "棉花数据库本身没有这一天的数据", "<kb "} {
		require.Contains(t, answer, want)
	}
	if !omitValue {
		require.Contains(t, answer, "5160")
		require.Contains(t, answer, "B3340")
		require.Contains(t, answer, "2023年9月21日")
		require.Equal(t, r.trace.Drafts[0], answer)
		return
	}
	primary, reportAndConclusion, ok := strings.Cut(answer, "\n\n")
	require.True(t, ok)
	_, originalReportAndConclusion, ok := strings.Cut(r.trace.Drafts[0], "\n\n")
	require.True(t, ok)
	require.NotContains(t, primary, "5160", "the no-plan variant cannot inject a missing primary value into the candidate")
	require.NotContains(t, primary, "B3340")
	require.Equal(t, originalReportAndConclusion, reportAndConclusion)
	require.NotContains(t, answer, "5160 张")
}
