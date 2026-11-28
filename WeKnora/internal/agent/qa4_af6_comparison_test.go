package agent

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type af6ComparisonArchive struct {
	af5ComparisonReplay
	ID, Candidate string
	Plan          *taskEvidencePlan
	PlanIssues    []string
	Declared      []taskCoverageTarget
	Citations     map[string]struct {
		CallID, ResultHash string
		Selector           map[string]any
	}
}

// The fixture projects the SQLite AgentSteps of Af5-1/2/3 KB-28 and
// Af5-1 KB-07/22, checked against the tool-result events. It holds their SSE
// and SQLite hashes, actual results and public answers. Directory display
// material and unused operations are omitted. Comparison target fields are
// reconstructed from the logged plans, including the broader metric.
// R1 preserves the returned visual description and elided body. R2 and R3
// restore the title and OCR omitted from events against the exact stored
// chunk IDs, source hash, body pointers and text in the archived Af3 page.
// R2 and R3 also retain the model draft that names the compared field and
// its original cell. Those drafts are inputs to attribution only.
// The structured controls retain their logged plans and accepted drafts,
// with the result selectors from the stored public citations.
// The extrema turn logs one live goal beyond its advisory plan. Its scope
// is reconstructed from the selected series and original SQL range.
func loadAf6ComparisonArchive(t *testing.T) []json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("testdata/qa4/replay/qa4_af6_comparison.json.gz")
	require.NoError(t, err)
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	require.NoError(t, err)
	defer reader.Close()
	var records []json.RawMessage
	require.NoError(t, json.NewDecoder(reader).Decode(&records))
	require.Len(t, records, 5)
	return records
}

func decodeAf6ComparisonArchive(t *testing.T, raw json.RawMessage) af6ComparisonArchive {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var record af6ComparisonArchive
	require.NoError(t, decoder.Decode(&record))
	return record
}

func TestQA4Af6ScopedComparisonReplay(t *testing.T) {
	for _, raw := range loadAf6ComparisonArchive(t) {
		confirmed := decodeAf6ComparisonArchive(t, raw)
		if confirmed.Target == nil {
			continue
		}
		for _, recovery := range []bool{false, true} {
			for _, shuffled := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/recovery=%v/shuffled=%v", confirmed.ID, recovery, shuffled), func(t *testing.T) {
					configureAf5Publication(t, true)
					t.Setenv("WEKNORA_AGENT_SCOPED_READ_RECOVERY", fmt.Sprint(recovery))
					t.Setenv("STRUCTURED_DATA_URL", "")
					t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", "")
					archived := decodeAf6ComparisonArchive(t, raw)
					archived.Target.PlanIssues = archived.PlanIssues
					archived.Target.UnresolvedComparisonSources = 2 - len(archived.Target.ComparisonSources)
					for _, step := range archived.Steps {
						for _, call := range step.ToolCalls {
							if shuffled && call.Name == "structured_query_series" {
								slices.Reverse(reviewAsSlice(call.Result.Data["rows"]))
							}
						}
					}
					engine, state, ctx := newAf5ComparisonEngine(t, archived.af5ComparisonReplay, shuffled)
					if archived.Candidate != "" {
						engine.turnReview.Last = candidateDecision{Candidate: archived.Candidate, Disposition: candidateRevise}
					}
					if archived.ID == "Af5-3/KB-28" {
						// In this turn the successful first read established the
						// document scope implicitly. Restore that selection without
						// fetching a new body or adding a fixture receipt.
						doc := archived.Documents[0]
						binder, err := engine.toolRegistry.GetTool(agenttools.ToolBindDocumentScope)
						require.NoError(t, err)
						args, err := json.Marshal(agenttools.DocumentScopePlan{
							Reason: "Restore the archived first explicit read target.",
							Sources: []agenttools.DocumentSourceSelection{
								{KnowledgeID: doc.ID, Title: doc.Title, FileName: doc.FileName},
							},
						})
						require.NoError(t, err)
						result, err := binder.Execute(ctx, args)
						require.NoError(t, err)
						require.True(t, result.Success, result.Error)
					}
					answer, complete := publishAf5StructuredTurn(t, ctx, engine, state, func() {
						engine.commitTerminalResult(ctx, state, "recorded", "", "", "failed", false, terminalTableEvidence)
					})
					require.Empty(t, engine.chatModel.(*mockChat).calls)
					require.True(t, engine.turnReview.TerminalReceipt.fromEvidence)
					require.Len(t, state.RoundSteps, len(archived.Steps))
					require.NotEmpty(t, answer)
					require.False(t, types.HasAgentAnswerMarkup(answer))
					if archived.ID == "Af5-1/KB-28" {
						if !recovery {
							require.Equal(t, archived.Final, answer)
							return
						}
						for _, expected := range []string{"298.93", "A87", "G87", "2023-10-31"} {
							require.Contains(t, answer, expected)
						}
						for _, unrelated := range []string{"331.2", "217.2", "231.93", "G86", "32.27", "高于", "这些结果未给出可对应所问范围的读数"} {
							require.NotContains(t, answer, unrelated)
						}
						return
					}
					for _, expected := range []string{"331.2", "298.93", "32.27", "第 6 页", "G87", "高于", "双方单位尚未核实为一致"} {
						require.Contains(t, answer, expected)
					}
					require.Nil(t, complete.Incomplete)
				})
			}
		}
	}
}

func TestQA4Af6ComparisonRepeatedSeriesRead(t *testing.T) {
	records := loadAf6ComparisonArchive(t)
	for _, shuffled := range []bool{false, true} {
		t.Run(fmt.Sprint(shuffled), func(t *testing.T) {
			configureAf5Publication(t, true)
			t.Setenv("WEKNORA_AGENT_SCOPED_READ_RECOVERY", "true")
			t.Setenv("STRUCTURED_DATA_URL", "")
			t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", "")
			wide, narrow := decodeAf6ComparisonArchive(t, records[0]), decodeAf6ComparisonArchive(t, records[1])
			require.Equal(t, "Af5-1/KB-28", wide.ID)
			require.Equal(t, "Af5-2/KB-28", narrow.ID)
			for _, step := range wide.Steps {
				for _, call := range step.ToolCalls {
					if call.Name == "structured_query_series" {
						narrow.Steps = append(narrow.Steps, types.AgentStep{Iteration: 7, ToolCalls: []types.ToolCall{call}})
					}
				}
			}
			engine, state, ctx := newAf5ComparisonEngine(t, narrow.af5ComparisonReplay, shuffled)
			engine.turnReview.Last = candidateDecision{Candidate: narrow.Candidate, Disposition: candidateRevise}
			answer, complete := publishAf5StructuredTurn(t, ctx, engine, state, func() {
				engine.commitTerminalResult(ctx, state, "recorded", "", "", "failed", false, terminalTableEvidence)
			})
			for _, expected := range []string{"331.2", "298.93", "32.27", "第 6 页", "G87", "高于"} {
				require.Contains(t, answer, expected)
			}
			require.NotContains(t, answer, "231.93")
			require.Nil(t, complete.Incomplete)
		})
	}
}

func TestQA4Af6ComparisonRetainsUnalignedReadings(t *testing.T) {
	records := loadAf6ComparisonArchive(t)
	for _, recovery := range []bool{false, true} {
		for _, focused := range []bool{false, true} {
			t.Run(fmt.Sprintf("recovery=%t/focused=%t", recovery, focused), func(t *testing.T) {
				configureAf5Publication(t, true)
				t.Setenv("WEKNORA_AGENT_SCOPED_READ_RECOVERY", fmt.Sprint(recovery))
				t.Setenv("STRUCTURED_DATA_URL", "")
				t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", "")
				archived := decodeAf6ComparisonArchive(t, records[1])
				require.Equal(t, "Af5-2/KB-28", archived.ID)
				// Select a different native document field from the same page.
				// Keep the original workbook call for the original field.
				archived.Target.Metric = "产量"
				if !focused {
					for _, step := range archived.Steps {
						for _, call := range step.ToolCalls {
							if call.Name == agenttools.ToolReadDocument {
								delete(call.Result.Data, "table_candidates")
							}
						}
					}
				}
				engine, state, ctx := newAf5ComparisonEngine(t, archived.af5ComparisonReplay, false)
				answer, _ := publishAf5StructuredTurn(t, ctx, engine, state, func() {
					engine.commitTerminalResult(ctx, state, "recorded", "", "", "failed", false, terminalTableEvidence)
				})
				require.Contains(t, answer, "产量第 6 页披露的 2023-10 数值为 116.0")
				if recovery {
					require.Contains(t, answer, "工商业期末库存 在 2023-10-31 的核实结果为 298.93")
					require.Contains(t, answer, "G87")
				} else {
					require.NotContains(t, answer, "G87")
				}
				for _, conclusion := range []string{"高于", "相差", "两处来源给出的数字不同"} {
					require.NotContains(t, answer, conclusion)
				}
			})
		}
	}
}

func TestQA4Af6PreservesStructuredPublicAnswers(t *testing.T) {
	for _, raw := range loadAf6ComparisonArchive(t) {
		archived := decodeAf6ComparisonArchive(t, raw)
		if archived.Target != nil {
			continue
		}
		for _, recovery := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/recovery=%v", archived.ID, recovery), func(t *testing.T) {
				setAf5StructuredReplayFlags(t, true)
				t.Setenv("WEKNORA_AGENT_SCOPED_READ_RECOVERY", fmt.Sprint(recovery))
				library := af5ReplayLibrary(t, archived.Steps)
				targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: library, TenantID: 1}}
				trace := af5StructuredTrace{Question: archived.Question, Steps: archived.Steps, Targets: archived.Plan.CoverageTargets}
				registry := qa4ReplayRegistry(archived.Steps...)
				engine, state, model, ctx, messages := prepareAf5StructuredTurn(t, trace, targets, registry, false)
				engine.taskPlan = archived.Plan
				engine.config.TaskPlanningEnabled = true
				engine.evidenceCompletion = newEvidenceCompletionTurn(0)
				engine.evidenceCompletion.declared = archived.Declared
				engine.refreshEvidenceCompletion(ctx, archived.Question, state, messages)
				candidate := archived.Candidate
				for old, selected := range archived.Citations {
					var match string
					for id, actual := range engine.turnReview.StructuredCitations {
						if actual.CallID == selected.CallID && actual.Reference.ResultHash == selected.ResultHash &&
							reviewJSONValuesEqual(actual.Directory["selector"], selected.Selector) {
							require.Empty(t, match)
							match = id
						}
					}
					require.NotEmpty(t, match, "the archived citation must resolve to the same selector on the same result")
					candidate = strings.ReplaceAll(candidate, old, match)
				}
				decision := engine.checkCandidate(ctx, archived.Question, candidate, "", state, messages, false)
				require.Equal(t, candidateAccepted, decision.Disposition, decision.ReasonCode)
				answer, complete := publishAf5StructuredTurn(t, ctx, engine, state, func() {
					engine.commitTerminalResult(ctx, state, "recorded", decision.Candidate, "", "answer", false, terminalNoFailure)
				})
				require.Zero(t, model.callCount)
				require.Len(t, state.RoundSteps, len(archived.Steps))
				require.Equal(t, archived.Final, answer)
				require.Nil(t, complete.Incomplete)
				require.Len(t, complete.EvidenceRefs, len(archived.Citations))
				if strings.HasSuffix(archived.ID, "/KB-07") {
					for _, expected := range []string{"231.93", "110", "298.93", "B87", "C87", "G87"} {
						require.Contains(t, answer, expected)
					}
				} else {
					require.Contains(t, answer, "17790 | 2023-09-01 | B4690")
					require.Contains(t, answer, "13830 | 2023-03-16 | B4575")
				}
			})
		}
	}
}

func TestQA4Af6ComparisonWindowMatches(t *testing.T) {
	const from, to = "2025-01-01", "2025-01-31"
	for _, test := range []struct {
		name, operation, start, end string
		rollback, recovery          bool
	}{
		{name: "native full window", operation: "query_series", start: from, end: to, rollback: true, recovery: true},
		{name: "native point", operation: "query_series", start: to, end: to, rollback: true, recovery: true},
		{name: "end inclusive", operation: "query_series", start: "2024-12-31", end: to, recovery: true},
		{name: "contains end", operation: "query_series", start: "2025-01-28", end: "2025-02-01", recovery: true},
		{name: "ends too early", operation: "query_series", start: from, end: "2025-01-30"},
		{name: "starts too late", operation: "query_series", start: "2025-02-01", end: "2025-02-07"},
		{name: "invalid start", operation: "query_series", start: "2025-01-00", end: to},
		{name: "no start", operation: "query_series", end: to},
		{name: "no end", operation: "query_series", start: from},
		{name: "SQL must still match", operation: "analyze_table", start: "2024-12-31", end: to},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := coverageEvidence{Operation: test.operation, PeriodFrom: test.start, PeriodTo: test.end}
			require.Equal(t, test.rollback, comparisonReadingWindowMatches(item, from, to, false))
			require.Equal(t, test.recovery, comparisonReadingWindowMatches(item, from, to, true))
		})
	}
}

func TestQA4Af6ScopedReadRecoverySwitch(t *testing.T) {
	for _, test := range []struct {
		name, scoped, disabledParent string
		enabled                      bool
	}{
		{name: "default", enabled: true},
		{name: "enabled", scoped: "true", enabled: true},
		{name: "disabled", scoped: "false"},
		{name: "verified publication disabled", scoped: "true", disabledParent: "WEKNORA_AGENT_VERIFIED_PUBLICATION"},
		{name: "relaxed evidence disabled", scoped: "true", disabledParent: "WEKNORA_AGENT_RELAXED_TERMINAL_EVIDENCE"},
		{name: "final read answer disabled", scoped: "true", disabledParent: "WEKNORA_AGENT_FINAL_READ_ANSWER"},
	} {
		for _, completion := range []string{"false", "true"} {
			t.Run(test.name+"/completion="+completion, func(t *testing.T) {
				configureAf5Publication(t, true)
				t.Setenv("WEKNORA_AGENT_SCOPED_READ_RECOVERY", test.scoped)
				t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", completion)
				if test.disabledParent != "" {
					t.Setenv(test.disabledParent, "false")
				}
				require.Equal(t, test.enabled, scopedReadRecoveryEnabled())
			})
		}
	}
}

func TestQA4Af6ComparisonDoesNotChooseBetweenUnattributedFields(t *testing.T) {
	configureAf5Publication(t, true)
	t.Setenv("WEKNORA_AGENT_SCOPED_READ_RECOVERY", "true")
	var engine AgentEngine
	target := taskCoverageTarget{StructuredTaskGoal: &StructuredTaskGoal{
		VersionTask: "compare_sources", Table: "Balances", Metric: "Stock",
		Period: map[string]any{"from": "2025-01", "to": "2025-01", "grain": "month"},
	}}
	reading := coverageEvidence{
		Kind: "cell", Operation: "query_series", Sheet: target.Table, SourceID: "workbook",
		SourceDomain: "structured", SeriesID: "series-a", Metric: "Closing stock", EvidenceID: "query-a",
		Date: "2025-01-31", Cell: "B4", DateCell: "A4", JSONPointer: "/data/rows/0/value",
		PeriodFrom: "2024-12-31", PeriodTo: "2025-01-31", Raw: json.Number("12"), Bound: true, RangeComplete: true,
	}
	record := coverageRecord{Evidence: []coverageEvidence{reading}}
	partial, aligned := engine.tableReceiptReadingsForDelivery(target, record, "", true)
	require.False(t, aligned)
	require.Empty(t, partial)
	partial, aligned = engine.tableReceiptReadingsWithDraftAttribution(target, record, "", true, &finalTableReadings{})
	require.False(t, aligned)
	require.Len(t, partial, 1)
	require.Equal(t, reading.Metric, partial[0].Evidence.Metric)
	other := reading
	other.SeriesID, other.Metric, other.EvidenceID, other.Cell = "series-b", "Opening stock", "query-b", "C4"
	record.Evidence = append(record.Evidence, other)
	partial, aligned = engine.tableReceiptReadingsWithDraftAttribution(target, record, "", true, &finalTableReadings{})
	require.False(t, aligned)
	require.Empty(t, partial)
}
