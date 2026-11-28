package agent

import (
	"compress/gzip"
	"encoding/json"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/agent/collaboration"
	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestQA4Af7SubstantiveAnswerFlag(t *testing.T) {
	for _, variant := range []struct {
		name, setting  string
		disabledParent string
		enabled        bool
	}{
		{name: "default", enabled: true},
		{name: "explicit rollback", setting: "false"},
		{name: "requires verified publication", setting: "true", disabledParent: "WEKNORA_AGENT_VERIFIED_PUBLICATION"},
		{name: "requires relaxed terminal evidence", setting: "true", disabledParent: "WEKNORA_AGENT_RELAXED_TERMINAL_EVIDENCE"},
		{name: "requires final read publication", setting: "true", disabledParent: "WEKNORA_AGENT_FINAL_READ_ANSWER"},
	} {
		t.Run(variant.name, func(t *testing.T) {
			setAf7StructuredReplayFlags(t, true)
			t.Setenv("WEKNORA_AGENT_SUBSTANTIVE_FINAL_ANSWER", variant.setting)
			if variant.disabledParent != "" {
				t.Setenv(variant.disabledParent, "false")
			}
			require.Equal(t, variant.enabled, substantiveFinalAnswerEnabled())
		})
	}
}

// Af6-3 supplies its own original inspect_source cells from the saved steps,
// checked against the tool-result events. Af6-1 did not persist agent steps,
// so its compact native results are projected from its own events. No body
// text or numerical reading is filled in from another run. The truncated
// comparison plan is represented by its recorded task, table and period.
func loadAf7PublicationReplay(t *testing.T) []af6ComparisonArchive {
	t.Helper()
	file, err := os.Open("testdata/qa4/replay/qa4_af7_publication.json.gz")
	require.NoError(t, err)
	defer file.Close()
	reader, err := gzip.NewReader(file)
	require.NoError(t, err)
	defer reader.Close()
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	var traces []af6ComparisonArchive
	require.NoError(t, decoder.Decode(&traces))
	require.Len(t, traces, 2)
	require.Equal(t, "da0e3b805e2e0d5de2f3a709b01cf18aee6a517dcb26cc3796f8a833f760d2ee", traces[0].SSEHash)
	require.Equal(t, "6d494400315c07e0497bea0cc5c8bec7c6d580b8575f052acb54cefb5567a5b5", traces[1].SSEHash)
	require.Equal(t, "8ffef7b33fdb4ab8d1f15f09d0f99ac6b04938b272156e8115963b732f0daa56", traces[1].StepsHash)
	return traces
}

func af7RestoreDocumentSelection(t *testing.T, engine *AgentEngine, trace af6ComparisonArchive) {
	t.Helper()
	// Both successful reads established this document selection implicitly.
	// Restore its actual document and title without executing a source read.
	doc := trace.Documents[0]
	binder, err := engine.toolRegistry.GetTool(agenttools.ToolBindDocumentScope)
	require.NoError(t, err)
	encoded, err := json.Marshal(agenttools.DocumentScopePlan{Reason: "Restore the recorded implicit source selection.", Sources: []agenttools.DocumentSourceSelection{
		{KnowledgeID: doc.ID, Title: doc.Title, FileName: doc.FileName},
	}})
	require.NoError(t, err)
	result, err := binder.Execute(t.Context(), encoded)
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
}

func TestQA4Af7InspectedComparisonPublication(t *testing.T) {
	for _, variant := range []string{"enabled", "earlier observation", "unrelated header", "different source version", "ambiguous nearby field", "rollback"} {
		t.Run(variant, func(t *testing.T) {
			setAf7StructuredReplayFlags(t, variant != "rollback")
			trace := loadAf7PublicationReplay(t)[1]
			require.Equal(t, "Af6-3/KB-28", trace.ID)
			require.Contains(t, trace.Final, "这些结果未给出可对应所问范围的读数")
			trace.Target.PlanIssues = trace.PlanIssues
			trace.Target.UnresolvedComparisonSources = 2
			if variant == "earlier observation" {
				trace.Target.Period["from"], trace.Target.Period["to"] = "2023-09-01", "2023-09-30"
			}
			inspections := 0
			for _, step := range trace.Steps {
				for _, call := range step.ToolCalls {
					require.NotEqual(t, "structured_query_series", call.Name)
					if call.Name == "structured_list_series" && variant == "ambiguous nearby field" {
						for _, field := range sourceNavigationRows(call.Result.Data["series"]) {
							if field["series_id"] == "balance.primary.G" {
								adjacent := maps.Clone(field)
								adjacent["series_id"], adjacent["metric"], adjacent["header_cells"] = "balance.primary.B", "工商业期初库存", []any{"B1"}
								call.Result.Data["series"] = append(reviewAsSlice(call.Result.Data["series"]), adjacent)
								break
							}
						}
					}
					if call.Name != "structured_inspect_source" {
						continue
					}
					inspections++
					if variant == "different source version" && inspections == 3 {
						call.Result.Data["sha256"] = strings.Repeat("c", 64)
					}
					if variant == "unrelated header" && inspections == 1 {
						for _, group := range sourceNavigationRows(call.Result.Data["ranges"]) {
							for _, cell := range sourceNavigationRows(group["cells"]) {
								if cell["cell"] == "G1" {
									cell["raw_value"], cell["value"] = "不同字段", "不同字段"
								}
							}
						}
					}
				}
			}
			require.Equal(t, 3, inspections)
			engine, state, ctx := newAf5ComparisonEngine(t, trace.af5ComparisonReplay, false)
			af7RestoreDocumentSelection(t, engine, trace)
			answer, complete := publishAf5StructuredTurn(t, ctx, engine, state, func() {
				engine.commitTerminalResult(ctx, state, "recorded", "", "", "failed", false, terminalTableEvidence)
			})
			require.Empty(t, engine.chatModel.(*mockChat).calls)
			require.True(t, complete.HasPartialAnswer())
			require.NotContains(t, answer, "331.2")
			require.NotContains(t, answer, "32.27")
			switch variant {
			case "enabled", "earlier observation":
				for _, expected := range []string{"棉花数据库.xlsx", "工商业期末库存", "棉花周报20240331.pptx", "未从", "无法判断两处是否一致"} {
					require.Contains(t, answer, expected)
				}
				if variant == "earlier observation" {
					require.Contains(t, answer, "231.93")
					require.Contains(t, answer, "G86")
					require.NotContains(t, answer, "298.93")
				} else {
					for _, expected := range []string{"298.93", "2023-10-31", "A87", "G87"} {
						require.Contains(t, answer, expected)
					}
					require.NotContains(t, answer, "G86")
				}
			case "rollback":
				require.Equal(t, trace.Final, answer)
			default:
				require.NotContains(t, answer, "298.93")
				require.NotContains(t, answer, "231.93")
				require.Contains(t, answer, "无法判断两处是否一致")
			}
		})
	}
}

func TestQA4Af7InspectedComparisonRequiresAlignedOperands(t *testing.T) {
	for _, matched := range []bool{true, false} {
		name := "different fields"
		if matched {
			name = "same field"
		}
		t.Run(name, func(t *testing.T) {
			setAf7StructuredReplayFlags(t, true)
			trace := loadAf7PublicationReplay(t)[1]
			trace.Target.PlanIssues = trace.PlanIssues
			trace.Target.UnresolvedComparisonSources = 2
			engine, state, ctx := newAf5ComparisonEngine(t, trace.af5ComparisonReplay, false)
			record, attempts := engine.projectTerminalCoverage(ctx, state, terminalCoverageOptions{finalRead: true, useDirectoryIdentity: true})
			book := inspectedComparisonReading(*trace.Target, record, attempts, "")
			require.True(t, book.Bound)
			// Independent verified report operand for this pairing control. It is
			// intentionally absent from the separate historical publication replay.
			report := coverageEvidence{Kind: "body", SourceDomain: "knowledge", SourceID: "test-report", SourceTitle: "Example report",
				KnowledgeBaseID: book.KnowledgeBaseID, EvidenceID: "test-report-receipt", JSONPointer: "/report", ReportedValue: "300",
				Sheet: book.Sheet, Metric: book.Metric, Date: book.Date, Grain: "month", Bound: true}
			if !matched {
				report.Metric = "another measure"
			}
			readings, aligned := engine.tableReceiptReadingsWithDraftAttribution(*trace.Target, record, "", true,
				&finalTableReadings{observations: []coverageEvidence{report}, inspected: book})
			require.Len(t, readings, 2)
			require.Equal(t, matched, aligned)
			delivery := terminalCoverageDelivery{chinese: true, relaxed: true, substantive: true, finalRead: true}
			for _, reading := range readings {
				delivery.facts = append(delivery.facts, terminalCoverageFact{coverageFact: reading, required: true})
			}
			if aligned {
				delivery.addComparison(readings, true)
			} else {
				delivery.addTableComparisonGap(*trace.Target, coverageCompletion{PinnedFacts: readings})
			}
			publication := delivery.renderPublication(terminalTableEvidence)
			require.Contains(t, publication.answer, "298.93")
			require.Contains(t, publication.answer, "300")
			require.NotContains(t, publication.answer, "未从另一来源读到")
			if aligned {
				require.Contains(t, publication.answer, "高于")
				require.False(t, publication.partial)
			} else {
				require.Contains(t, publication.answer, "无法判断两处是否一致")
				require.True(t, publication.partial)
			}
		})
	}
}

func TestQA4Af7InterruptedComparisonPublication(t *testing.T) {
	for _, variant := range []string{"enabled", "rollback", "revoked document"} {
		t.Run(variant, func(t *testing.T) {
			setAf7StructuredReplayFlags(t, variant != "rollback")
			trace := loadAf7PublicationReplay(t)[0]
			require.Equal(t, "Af6-1/KB-28", trace.ID)
			engine, state, ctx := newAf5ComparisonEngine(t, trace.af5ComparisonReplay, false)
			af7RestoreDocumentSelection(t, engine, trace)
			_, binding := af3CollaborationReplayStore(t)
			require.NoError(t, binding.Runtime.Store.Change(ctx, binding.Scope, func(group *collaboration.Group) error {
				group.ParentMessageID = binding.Scope.ID
				return nil
			}))
			engine.SetCollaboration(binding)
			for _, step := range state.RoundSteps {
				engine.checkpointInterruptedEvidence(ctx, step.ToolCalls, binding.Scope.ID)
			}
			group, err := binding.Runtime.Store.Read(ctx, binding.Scope)
			require.NoError(t, err)
			require.True(t, group.ParentEvidenceCurrent)
			require.Len(t, group.ParentEvidence, 2)
			authorized := func(source InterruptedSource) bool {
				return source.KnowledgeBaseID == af3KB28Library && (source.Structured || variant != "revoked document")
			}
			answer := RenderInterruptedEvidence(ctx, group.ParentEvidence, authorized, false, InterruptedProgress{Known: true})
			require.Contains(t, answer, "298.93")
			require.Contains(t, answer, "G87")
			require.NotContains(t, answer, "331.2")
			switch variant {
			case "rollback":
				require.Equal(t, trace.Final, answer)
			case "revoked document":
				require.NotContains(t, answer, "棉花周报20240331.pptx")
				require.NotContains(t, answer, "读数是否一致")
			default:
				require.Contains(t, answer, "未从 棉花周报20240331.pptx 的已读片段")
				require.Contains(t, answer, "查到可与上面读数核对的数")
				require.NotContains(t, answer, "本轮执行中断，查证未完成")
			}
		})
	}
}

func TestQA4Af7DirectoryOnlyPublication(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		name := "rollback"
		if enabled {
			name = "substantive"
		}
		t.Run(name, func(t *testing.T) {
			setAf7StructuredReplayFlags(t, enabled)
			trace := loadAf7StructuredArchive(t).Interrupted
			require.Equal(t, "Af6-1/KB-22", trace.ID)
			for _, step := range trace.Steps {
				require.False(t, slices.ContainsFunc(step.ToolCalls, func(call types.ToolCall) bool { return call.Name == "structured_query_series" }))
			}
			trace.Targets = []taskCoverageTarget{{RequestBasis: trace.Question, StructuredTaskGoal: &StructuredTaskGoal{
				SourceMode: "open", SourceTime: "observed_period", Table: "郑棉期货", Metric: "收盘价",
				Period: map[string]any{"from": "2023-01-01", "grain": "day"}, Required: []string{"scoped_value"},
				Delivery: map[string]any{"aggregation": "min_max"},
			}}}
			library := af5ReplayLibrary(t, trace.Steps)
			targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: library, TenantID: 1}}
			engine, state, _, ctx, _ := prepareAf5StructuredTurn(t, trace, targets, qa4ReplayRegistry(), false)
			engine.turnReview.FinalCoverageReadAttempted = true
			answer, complete := publishAf5StructuredTurn(t, ctx, engine, state, func() {
				engine.commitTerminalResult(ctx, state, "recorded", "", "", "failed", false, terminalAnswerEvidence)
			})
			require.True(t, complete.HasPartialAnswer())
			for _, absent := range []string{"17790", "13830", "structured_list_series", "futures.close.CF.CZC"} {
				require.NotContains(t, answer, absent)
			}
			if !enabled {
				require.Equal(t, trace.Final, answer)
				return
			}
			for _, expected := range []string{"棉花数据库.xlsx", "郑棉期货", "收盘价", "目录列出的日期范围", "2023-09-22", "最高值、最低值及出现日期尚无法确定"} {
				require.Contains(t, answer, expected)
			}
			require.NotContains(t, answer, "这些结果未给出可对应所问范围的读数")
		})
	}
}
