package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// A-1 #6/#17/#32 and A-3 #6/#19/#32 are the original bind, page and
// series invocations. The source repository supplies the actual OCR chunk
// because the event stream drops its text. IDs were aliased and the v2
// hashes recomputed. Four original drafts came from the messages table.
func TestQA4Af1RecordedTablePublication(t *testing.T) {
	for _, round := range []int{1, 3} {
		for _, completed := range []bool{false, true} {
			for _, modern := range []bool{false, true} {
				for _, variant := range []string{"empty", "original", "bounded edit"} {
					name := fmt.Sprintf("A-%d/completion_%t/document_%t/%s", round, completed, modern, variant)
					t.Run(name, func(t *testing.T) {
						// Reconstruct the actual historical (A3) read. Publication
						// must keep its already-issued receipt with either switch.
						t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
						r := newQA4Stage2CReplay(t, "af1-kb28", completed, false)
						r.ctx = context.WithValue(r.ctx, types.LanguageContextKey, "zh-CN")
						r.engine.config.StructuredResultReviewEnabled = false
						metric := "期末库存"
						if variant == "empty" {
							// Given the actual native field in the existing plan, the
							// no-draft variant can use the exact field outright.
							metric = "工商业期末库存"
						}
						// The log trims task_plan_ready at 512 bytes. Reconstruct its
						// witnessed unverified_primary_ref omission at the actual
						// planner entry; the remaining scope is visible in the question,
						// final coverage note and tool results. No evidence roles are added.
						plan := qa4PlanReply(t, map[string]any{
							"goal_index": 0, "request_basis": r.trace.Question, "version_task": "compare_sources",
							"source_mode": "open", "source_time": "observed_period", "primary_ref": "棉花周报20240331.pptx",
							"table": "中国棉花平衡表", "metric": metric, "comparison_sources": []string{"3月31日周报", "棉花数据库"},
							"period": map[string]any{"grain": "month", "from": "2023-10", "to": "2023-10"}, "required": []string{"scoped_value"},
						}, "knowledge", "structured")
						planner := &qa4ReplayModel{t: t, plan: json.RawMessage(plan), counts: map[string]int{}}
						r.engine.chatModel = planner
						r.engine.prepareTaskPlanning()
						r.messages = r.engine.planWorkbenchTask(r.ctx, r.state, r.trace.Question, r.messages, "historical-partial")
						require.Equal(t, 1, planner.counts["agent_task_plan"])
						require.Len(t, r.engine.taskPlan.CoveragePlanIssues, 1)
						require.Contains(t, r.engine.taskPlan.CoveragePlanIssues[0].Reasons, "unverified_primary_ref")
						require.Empty(t, r.engine.taskPlan.CoverageTargets[0].PrimaryRef)
						r.engine.chatModel = r.model
						calls := r.trace.Calls[:3]
						if round == 3 {
							calls = r.trace.Calls[3:]
						}
						r.calls(calls[:1])
						r.calls(calls[1:2])
						r.calls(calls[2:])
						report := r.results[calls[1].Event].Result
						require.True(t, report.Success, report.Error)
						require.NotEmpty(t, report.Data["table_reads"])
						var citation string
						for _, item := range r.engine.evidenceCompletion.record.Evidence {
							if item.Cell == "G87" && item.CitationID != "" {
								citation = item.CitationID
							}
						}
						require.NotEmpty(t, citation)
						t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", strconv.FormatBool(modern))
						var completion event.AgentCompleteData
						r.engine.eventBus.On(event.EventAgentComplete, func(_ context.Context, emitted event.Event) error {
							completion = emitted.Data.(event.AgentCompleteData)
							return nil
						})
						var draft string
						switch variant {
						case "original":
							index := 0
							if round == 3 {
								index = 1
							}
							draft = strings.ReplaceAll(r.trace.Drafts[index], "replay-executed-structured-citation", citation)
							// Check the original model draft with the normal candidate
							// and publish it only if the same outlet would accept it.
							r.run(chatAnswer(draft))
							require.False(t, r.state.IsComplete, "the recorded draft includes wrong or unsupported claims")
						case "bounded edit":
							// An explicit offline edit of the recorded comparison keeps
							// its conclusion, exact operands and citations, removing the
							// unverified extras before exercising draft preservation.
							draft = "两处来源刊载的读数不一样。\n\n" +
								`棉花周报20240331.pptx 第6页“中国棉花平衡表”的 2023-10 行，“工商业期末库存”为 331.2。<kb doc="棉花周报20240331.pptx" chunk_id="replay-comparison-page" kb_id="replay-library" />` + "\n\n" +
								`棉花数据库.xlsx 中国棉花平衡表在 2023-10-31（A87）记录的“工商业期末库存”为 298.93（G87）。<evidence evidence_id="` + citation + `" />` + "\n\n两处单位均待确认。"
							decision := r.engine.checkCandidate(r.ctx, r.trace.Question, draft, "", r.state, r.messages, false)
							require.True(t, slices.Contains([]candidateDisposition{candidateAccepted, candidateEvidencePartial}, decision.Disposition), "%+v", decision)
							r.engine.commitTerminalResult(r.ctx, r.state, "s2c", draft, "", "partial", false)
						}
						r.exhaust()
						answer := r.answer()
						if variant == "bounded edit" {
							require.Equal(t, draft, answer)
						} else {
							require.Contains(t, answer, "原报数值不同")
							require.Equal(t, 1, strings.Count(answer, "331.2"))
						}
						for _, witness := range []string{"331.2", "298.93", "2023-10-31", "A87", "G87", "棉花周报", "棉花数据库"} {
							require.Contains(t, answer, witness)
						}
						require.NotContains(t, answer, "万吨")
						require.NotContains(t, answer, "217.2")
						require.Zero(t, r.model.reviewCalls)
						r.engine.emitCompletionEvent(r.ctx, r.state, "s2c", "turn", time.Now())
						require.Equal(t, answer, completion.FinalAnswer)
					})
				}
			}
		}
	}
}

// A-1 KB-07 #14/#15 selects and returns three native workbook fields.
// Preserve the exact transport range and v2 payload with IDs aliased.
func TestQA4Af1RecordedSameRowSQLPublication(t *testing.T) {
	for _, completed := range []bool{false, true} {
		for _, mutation := range []string{"original", "different date cell", "modified value"} {
			t.Run(fmt.Sprintf("completion_%t/%s", completed, mutation), func(t *testing.T) {
				r := newQA4Stage2CReplay(t, "af1-kb07", completed, false)
				r.ctx = context.WithValue(r.ctx, types.LanguageContextKey, "zh-CN")
				r.through(15)
				if mutation != "original" {
					data := r.results[15].Result.Data
					rows := data["rows"].([]any)
					row := rows[1].([]any)
					if mutation == "different date cell" {
						row[4] = "A88"
					} else {
						row[2] = json.Number("111")
					}
					require.NoError(t, rehashCoverageSQLFixture(data))
				}
				r.exhaust()
				if mutation != "original" {
					require.Empty(t, r.state.FinalAnswer)
					require.NotEmpty(t, r.engine.turnReview.TerminalReceipt.notice)
					return
				}
				answer := r.answer()
				for _, witness := range []string{"2023-10-31", "231.93", "110", "298.93", "棉花数据库.xlsx", "中国棉花平衡表", "A87", "B87", "C87", "G87"} {
					require.Contains(t, answer, witness)
				}
				require.NotContains(t, answer, "万吨")
				require.Zero(t, r.model.reviewCalls)
			})
		}
	}
}

func TestQA4Af1ReceiptPairKeepsScopeAndUnits(t *testing.T) {
	for _, variant := range []string{"third source", "one pinned source", "same title in another library", "same table in another document", "wrong field", "daily ledger", "incompatible units"} {
		t.Run(variant, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
			r := newQA4Stage2CReplay(t, "af1-kb28", false, false)
			calls := r.trace.Calls[1:3]
			if variant == "third source" || variant == "same title in another library" {
				r.calls(r.trace.Calls[:1]) // The recorded binding selects two physical documents.
			}
			r.calls(calls[:1])
			r.calls(calls[1:])
			record, _ := r.engine.terminalCoverageRecord(r.ctx, r.state)
			target := taskCoverageTarget{StructuredTaskGoal: &StructuredTaskGoal{
				VersionTask: "compare_sources", SourceMode: "open", Table: "中国棉花平衡表", Metric: "工商业期末库存",
				Period: map[string]any{"grain": "month", "from": "2023-10", "to": "2023-10"},
			}}
			readings, aligned := r.engine.tableReceiptReadings(target, record, "")
			require.True(t, aligned)
			require.Len(t, readings, 2)
			if variant == "one pinned source" {
				pinned := &SelectedDocumentInfo{KnowledgeID: "other-report", KnowledgeBaseID: qa4Stage2Library}
				r.engine.selectedDocs = []*SelectedDocumentInfo{pinned}
				target.ComparisonSources = []string{r.engine.modelContext.RegisterDocument(pinned.KnowledgeID)}
			}
			var draft string
			for i := range record.Evidence {
				item := &record.Evidence[i]
				switch {
				case item.Kind == "body" && item.TableGrid != nil:
					switch variant {
					case "same table in another document":
						peer := *item
						peer.SourceID = "another-document"
						record.Evidence = append(record.Evidence, peer)
						coverageRememberSnapshot(record.Snapshots, peer)
					case "incompatible units":
						item.Unit, item.UnitBasis = "kg", "field_header"
					}
				case item.Kind == "cell" && item.Cell == "G87":
					switch variant {
					case "third source":
						item.KnowledgeID = "third-workbook"
					case "same title in another library":
						item.KnowledgeBaseID = "third-library"
						coverageRememberSnapshot(record.Snapshots, *item)
					case "wrong field":
						item.Metric, item.Cell, item.Column, item.Raw = "工商业期初库存", "B87", "B", json.Number("217.2")
						draft = "棉花数据库.xlsx 在 2023-10-31 的工商业期初库存是 217.2（B87）。"
					case "daily ledger":
						item.Grain, item.GrainFromLookup = "day", false
					case "incompatible units":
						item.Unit, item.UnitBasis = "t", "field_header"
					}
				}
			}
			readings, aligned = r.engine.tableReceiptReadings(target, record, draft)
			if variant == "incompatible units" {
				require.True(t, aligned, "original readings can be compared as printed numerals")
				d := terminalCoverageDelivery{chinese: true}
				d.addComparison(readings, true)
				require.Equal(t, []string{"两处来源的原报数值不同。原单位不同，无法直接比较量值。"}, d.conclusions)
				return
			}
			require.False(t, aligned)
			if variant == "daily ledger" || variant == "one pinned source" {
				require.Len(t, readings, 2, "both operands remain available with their independent original coordinates")
			} else {
				require.Len(t, readings, 1)
			}
		})
	}
}
