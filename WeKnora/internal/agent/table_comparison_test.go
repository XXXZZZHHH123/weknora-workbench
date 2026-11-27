package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/stretchr/testify/require"
)

func TestA3TableComparisonPublicationUsesCheckedSide(t *testing.T) {
	for _, mode := range []string{"undeclared", "partial", "unfounded verdict", "model-written partial"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
			r := newQA4Stage2CReplay(t, "s2-4-kb28", false, false)
			if mode != "undeclared" {
				r.setPlan(StructuredTaskGoal{
					SourceMode: "open", SourceTime: "observed_period", VersionTask: "compare_sources",
					Table: "中国棉花平衡表", Metric: "工商业期末库存", ComparisonSources: []string{"棉花数据库"},
					Period: map[string]any{"grain": "month", "from": "2023-10", "to": "2023-10"}, Required: []string{"scoped_value"},
				})
				r.engine.taskPlan.CoverageTargets[0].UnresolvedComparisonSources = 1
			}
			r.through(33)
			require.True(t, r.results[33].Result.Success)
			draft := qa4Stage2EComparisonDraft(t, r, 21)
			if mode != "undeclared" {
				var selected coverageEvidence
				for _, fact := range r.engine.evidenceCompletion.record.Evidence {
					if fact.Kind == "cell" && fact.Cell == "G87" {
						selected = fact
						break
					}
				}
				require.NotEmpty(t, selected.EvidenceID)
				if mode == "model-written partial" {
					citation := r.engine.modelContext.CitableEvidenceHandle(selected.CitationID)
					require.NotEmpty(t, citation)
					draft = "棉花数据库的中国棉花平衡表中，2023-10-31 的工商业期末库存为 298.93（G87）。<ref id=\"" +
						citation + "\"/>\n\n周报已读部分尚缺同表同期的工商业期末库存，无法确认两份材料给出的数值是否一致。"
				}
				if mode == "unfounded verdict" {
					draft = "两份结果一致。"
				}
				draft = r.withDeclaration(draft, map[string]any{
					"goal_index": 0, "role": "primary", "source_ref": "棉花数据库", "selection_ref": coverageObservationRef(selected),
					"grain": "month", "period_from": "2023-10-01", "period_to": "2023-10-31",
				})
			}
			var completed event.AgentCompleteData
			r.engine.eventBus.On(event.EventAgentComplete, func(_ context.Context, item event.Event) error {
				completed = item.Data.(event.AgentCompleteData)
				return nil
			})
			r.run(chatAnswer(draft))
			if mode == "model-written partial" {
				require.True(t, r.state.IsComplete)
				require.Equal(t, checkedPartialComparisonReason, r.engine.turnReview.Last.ReasonCode)
			} else {
				require.False(t, r.state.IsComplete)
				require.Equal(t, "table_evidence_attribution_incomplete", r.engine.turnReview.Last.ReasonCode)
			}
			r.exhaust()
			answer := r.answer()
			require.Contains(t, answer, "298.93")
			require.NotContains(t, documentNumericProse(answer), "876")
			require.NotContains(t, documentNumericProse(answer), "331.2")
			require.NotContains(t, answer, "两份结果一致。")
			for _, published := range r.finals {
				require.NotContains(t, documentNumericProse(published.Content), "876")
			}
			r.engine.emitCompletionEvent(r.ctx, r.state, "s2c", "turn", time.Now())
			require.Equal(t, answer, completed.FinalAnswer)
		})
	}
}

func TestA3TableComparisonUsesActualReportAndWorkbookReads(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", strconv.FormatBool(enabled))
			r := newQA4Stage2CReplay(t, "s2-2-kb28", !enabled, false)
			r.through(30)
			read := r.results[30].Result
			require.True(t, read.Success, read.Error)
			if enabled {
				require.NotEmpty(t, read.Data["table_reads"])
			} else {
				require.NotContains(t, read.Data, "table_reads")
			}
			table, metric := "中国棉花平衡表", "工商业期末库存"
			target := taskCoverageTarget{StructuredTaskGoal: &StructuredTaskGoal{
				VersionTask: "compare_sources", Table: table, Metric: metric, ComparisonSources: []string{"report", "workbook"},
				Period: map[string]any{"grain": "month", "from": "2023-10", "to": "2023-10"},
			}}
			record := r.engine.evidenceCompletion.record
			bookIdx := slices.IndexFunc(record.Evidence, func(item coverageEvidence) bool { return item.Cell == "G87" && item.Kind == "cell" })
			reportIdx := slices.IndexFunc(record.Evidence, func(item coverageEvidence) bool {
				return item.Object != "" && item.Page == 6 && ((!enabled && item.TableGrid == nil) || item.Sheet == table && item.TableGrid != nil)
			})
			require.NotEqual(t, -1, bookIdx)
			require.NotEqual(t, -1, reportIdx)
			book, report := record.Evidence[bookIdx], record.Evidence[reportIdx]
			bookBinding := comparisonFixtureBinding(book, "workbook")
			bookBinding.Grain, bookBinding.PeriodFrom, bookBinding.PeriodTo = "month", "2023-10-01", "2023-10-31"
			reportBinding := comparisonFixtureBinding(report, "report")
			reportBinding.Date, reportBinding.Metric = "2023-10", metric
			record.Bindings = []coverageBinding{bookBinding}
			oneSide := evaluateTableComparison(target, record)
			require.Equal(t, coverageOpen, oneSide.State)
			if !enabled {
				// This older full-month result did not label the native grain.
				// The A2 compatibility path still needs an exact-day receipt.
				require.Equal(t, []string{"report", "workbook"}, oneSide.OpenItems)
				require.Empty(t, oneSide.PinnedFacts)
				return
			}
			require.Equal(t, []string{"report", "workbook"}, oneSide.OpenItems)
			require.Len(t, oneSide.PinnedFacts, 1)
			require.True(t, oneSide.PinnedFacts[0].Evidence.ComparisonGrainUnverified)
			require.Empty(t, checkedTableComparison(target, oneSide).Result)
			record.Bindings = append(record.Bindings, reportBinding)
			for range 2 {
				result := evaluateTableComparison(target, record)
				require.Equal(t, coverageOpen, result.State)
				require.Equal(t, "comparison_grain_unverified", result.Reason)
				require.Equal(t, []string{"workbook"}, result.OpenItems)
				require.Empty(t, checkedTableComparison(target, result).Result)
				require.False(t, checkedTableComparison(target, result).SameUnit)
				require.Equal(t, "331.2", result.PinnedFacts[0].Evidence.ReportedValue)
				require.Equal(t, "G87", result.PinnedFacts[1].Evidence.Cell)
				slices.Reverse(record.Evidence)
				slices.Reverse(record.Bindings)
			}
			// The captured response predates original date-format reporting. Add
			// the shape of a newer hash-verified read of that exact date cell.
			base := book
			base.Operation, base.Status, base.EvidenceID = "inspect_source", "ok", "original-date-read"
			base.ToolCallID = "inspect-date"
			format := map[string]any{"sheet": table, "cell": book.DateCell, "value_status": "original",
				"value": book.Date + "T00:00:00", "number_format": `yyyy\-mm;@`}
			data := map[string]any{"sha256": book.SnapshotHash, "hash_verified": true,
				"ranges": []any{map[string]any{"cells": []any{format}}}}
			dateReads := coverageInspectionEvidence(base, nil, data)
			require.Len(t, dateReads, 1)
			verifiedDate := dateReads[0]
			for _, variation := range []struct {
				name   string
				change func(*coverageEvidence)
				valid  bool
			}{
				{"same workbook version and date cell", func(_ *coverageEvidence) {}, true},
				{"renamed original workbook", func(item *coverageEvidence) { item.SourceTitle = "Renamed" }, true},
				{"other snapshot", func(item *coverageEvidence) { item.SnapshotHash = strings.Repeat("e", 64) }, false},
				{"other sheet", func(item *coverageEvidence) { item.Sheet += " B" }, false},
				{"other date cell", func(item *coverageEvidence) { item.Cell = "A90" }, false},
				{"other stored date", func(item *coverageEvidence) { item.Raw = "2023-10-01T00:00:00" }, false},
				{"original daily format", func(item *coverageEvidence) { item.OriginalNumberFormat = "yyyy-mm-dd" }, false},
				{"text only display", func(item *coverageEvidence) { item.OriginalNumberFormat = `"2023-10"` }, false},
			} {
				t.Run(variation.name, func(t *testing.T) {
					inspection := verifiedDate
					variation.change(&inspection)
					withRead := record
					withRead.Evidence = append(slices.Clone(record.Evidence), inspection)
					result := evaluateTableComparison(target, withRead)
					require.Len(t, result.PinnedFacts, 2)
					bookSide := result.PinnedFacts[1].Evidence
					require.Equal(t, book.Date, bookSide.Date, "the original date is retained along with the label")
					if variation.valid {
						require.Equal(t, coverageSatisfied, result.State)
						require.Equal(t, "2023-10", bookSide.Period)
						require.Contains(t, bookSide.TablePeriodBasisRef, "/number_format")
						require.Equal(t, "different", checkedTableComparison(target, result).Result)
						require.Equal(t, "unknown", checkedTableComparison(target, result).UnitStatus)
					} else {
						require.Equal(t, coverageOpen, result.State)
						require.True(t, bookSide.ComparisonGrainUnverified)
						require.Empty(t, checkedTableComparison(target, result).Result)
					}
				})
			}
		})
	}
}

func comparisonFixtureBinding(item coverageEvidence, side string) coverageBinding {
	binding := coverageBinding{Role: "primary", SourceRef: side, SourceID: item.SourceID, PhysicalSourceID: comparisonPhysicalSource(item)[1],
		KnowledgeBaseID: item.KnowledgeBaseID, SnapshotHash: item.SnapshotHash, Sheet: item.Sheet, ChunkID: item.ChunkID, Page: item.Page,
		Object: item.Object, EvidenceID: item.EvidenceID, JSONPointer: item.JSONPointer, Metric: item.Metric}
	if item.TableGrid != nil {
		binding.TableObjectGrid = strconv.Itoa(*item.TableGrid)
	}
	return binding
}

func TestA3TableComparisonKeepsVerifiedSide(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	target := taskCoverageTarget{StructuredTaskGoal: &StructuredTaskGoal{
		VersionTask: "compare_sources", Table: "Inventory", Metric: "Closing balance", ComparisonSources: []string{"release", "ledger"},
		Period: map[string]any{"grain": "month", "from": "2025-04", "to": "2025-04"},
	}}
	const reportText = "Month\tClosing balance\tInbound\n2025-04\t25\t25"
	for _, test := range []struct {
		name       string
		change     func(*taskCoverageTarget, *coverageEvidence, *coverageBinding, *coverageEvidence)
		wantState  string
		wantReason string
		pins       int
	}{
		{"renamed and relocated", func(_ *taskCoverageTarget, report *coverageEvidence, _ *coverageBinding, book *coverageEvidence) {
			report.SourceTitle, report.Page, book.SourceTitle = "Z", 19, "A"
		}, coverageSatisfied, "matching_primary", 2},
		{"explicit workbook period label", func(_ *taskCoverageTarget, _ *coverageEvidence, _ *coverageBinding, book *coverageEvidence) {
			book.Grain, book.DateMode, book.Period = "", "period", "2025-04"
		}, coverageSatisfied, "matching_primary", 2},
		{"adjacent table", func(_ *taskCoverageTarget, report *coverageEvidence, _ *coverageBinding, _ *coverageEvidence) {
			report.Sheet = "Capacity"
		}, coverageOpen, "comparison_source_unverified", 1},
		{"same value in unselected column", func(_ *taskCoverageTarget, _ *coverageEvidence, selection *coverageBinding, _ *coverageEvidence) {
			selection.Metric = "Inbound"
		}, coverageOpen, "comparison_source_unverified", 1},
		{"year row is not a month", func(_ *taskCoverageTarget, report *coverageEvidence, selection *coverageBinding, _ *coverageEvidence) {
			report.Raw = strings.ReplaceAll(reportText, "2025-04", "2025")
			selection.Date = "2025"
		}, coverageOpen, "comparison_source_unverified", 1},
		{"day row is not a month", func(_ *taskCoverageTarget, report *coverageEvidence, selection *coverageBinding, _ *coverageEvidence) {
			report.Raw = strings.ReplaceAll(reportText, "2025-04", "2025-04-30")
			selection.Date = "2025-04-30"
		}, coverageOpen, "comparison_source_unverified", 1},
		{"different verified unit", func(_ *taskCoverageTarget, report *coverageEvidence, _ *coverageBinding, _ *coverageEvidence) {
			report.Unit = "t"
		}, coverageOpen, "comparison_units_incompatible", 2},
		{"unsupported unit asserted by model", func(_ *taskCoverageTarget, report *coverageEvidence, selection *coverageBinding, _ *coverageEvidence) {
			report.Unit, report.UnitBasis, selection.Unit = "", "", "kg"
		}, coverageOpen, "comparison_source_unverified", 1},
		{"body was never selected as a table", func(_ *taskCoverageTarget, report *coverageEvidence, _ *coverageBinding, _ *coverageEvidence) {
			report.TableGrid = nil
		}, coverageOpen, "comparison_source_unverified", 1},
		{"same workbook through both APIs", func(_ *taskCoverageTarget, report *coverageEvidence, _ *coverageBinding, book *coverageEvidence) {
			book.KnowledgeID = report.SourceID
		}, coverageOpen, "comparison_scope_conflict", 2},
		{"unresolved release reference", func(target *taskCoverageTarget, _ *coverageEvidence, _ *coverageBinding, _ *coverageEvidence) {
			target.ComparisonSources, target.UnresolvedComparisonSources = []string{"ledger"}, 1
		}, coverageOpen, "comparison_source_unresolved", 1},
		{"duplicate monthly row", func(_ *taskCoverageTarget, report *coverageEvidence, _ *coverageBinding, _ *coverageEvidence) {
			report.Raw = reportText + "\n2025-04\t25\t25"
		}, coverageOpen, "comparison_source_unverified", 1},
		{"ragged duplicate monthly row", func(_ *taskCoverageTarget, report *coverageEvidence, _ *coverageBinding, _ *coverageEvidence) {
			report.Raw = reportText + "\n2025-04\t25"
		}, coverageOpen, "comparison_source_unverified", 1},
		{"duplicate selected header", func(_ *taskCoverageTarget, report *coverageEvidence, _ *coverageBinding, _ *coverageEvidence) {
			report.Raw = strings.ReplaceAll(reportText, "Inbound", "Closing balance")
		}, coverageOpen, "comparison_source_unverified", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			goal := *target.StructuredTaskGoal
			target := taskCoverageTarget{StructuredTaskGoal: &goal}
			book := coverageEvidence{SourceDomain: "structured", SourceID: "dataset", KnowledgeID: "book", KnowledgeBaseID: "library", Kind: "cell",
				Operation: "query_series", EvidenceID: "book-read", JSONPointer: "/value", SnapshotHash: "book-snapshot", Bound: true,
				Sheet: target.Table, Metric: target.Metric, Date: "2025-04-30", DateCell: "A15", Cell: "B15", Grain: "month",
				Unit: "kg", UnitBasis: "field_header", Raw: json.Number("25")}
			grid := 0
			report := coverageEvidence{SourceDomain: "knowledge", SourceID: "report", KnowledgeBaseID: book.KnowledgeBaseID, Kind: "body",
				Operation: agenttools.ToolReadDocument, EvidenceID: "report-read", JSONPointer: "/text", SnapshotHash: "report-snapshot",
				Bound: true, Complete: true, Page: 5, ChunkID: "body", TableTitleChunkID: "heading", Object: "table-object", TableGrid: &grid,
				Sheet: target.Table, Unit: "kg", UnitBasis: "field_header", Raw: reportText}
			selection := coverageBinding{Date: "2025-04", Metric: target.Metric}
			test.change(&target, &report, &selection, &book)
			report.ContentHash = candidateTextDigest(report.Raw.(string))
			bind := comparisonFixtureBinding(report, "release")
			bind.Date, bind.Metric, bind.Unit = selection.Date, selection.Metric, selection.Unit
			input := coverageRecord{Evidence: []coverageEvidence{report, book}, Bindings: []coverageBinding{bind, comparisonFixtureBinding(book, "ledger")}}
			result := evaluateTableComparison(target, input)
			require.Equal(t, test.wantState, result.State)
			require.Equal(t, test.wantReason, result.Reason)
			require.Len(t, result.PinnedFacts, test.pins)
			check := checkedTableComparison(target, result)
			if test.wantState == coverageSatisfied {
				require.Equal(t, "equal", check.Result)
				require.True(t, check.SameUnit)
			} else {
				require.Empty(t, check.Result)
			}
			require.Contains(t, result.PinnedFacts[len(result.PinnedFacts)-1].Evidence.ComparisonSourceRef, "ledger")
		})
	}
}

func TestA3FactIdentityKeepsNativeReceiptsAndDistinctEqualColumns(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	item := coverageEvidence{Kind: "cell", SourceID: "dataset", KnowledgeID: "workbook", KnowledgeBaseID: "library", SnapshotHash: "workbook-a",
		Sheet: "Operations", Date: "2025-04-30", DateCell: "A13", PeriodFrom: "2025-04-01", PeriodTo: "2025-04-30",
		Metric: "Closing balance", Subject: "Warehouse", Column: "B", Cell: "B13", Unit: "kg", UnitBasis: "field_header",
		EvidenceID: "one", ToolCallID: "read-one", JSONPointer: "/data/rows/0/raw", Raw: json.Number("25")}
	secondRead := item
	secondRead.EvidenceID, secondRead.ToolCallID, secondRead.JSONPointer = "two", "read-two", "/data/rows/7/raw"
	secondRead.PeriodFrom, secondRead.Raw = "2025-02-01", "25.0"
	otherField := item
	otherField.Column, otherField.Cell, otherField.Metric = "C", "C13", "Inbound"
	otherVersion := item
	otherVersion.SnapshotHash = "workbook-b"
	otherUnit := item
	otherUnit.Unit = "t"
	otherDimension := item
	otherDimension.Dimensions = map[string]any{"block": "secondary"}
	var result coverageCompletion
	for _, read := range []coverageEvidence{item, secondRead, otherField, otherVersion, otherUnit, otherDimension} {
		result.pin("scoped_value", read)
	}
	require.Len(t, result.PinnedFacts, 5)
	require.Len(t, result.PinnedFacts[0].Evidence.Receipts, 2)
	require.Len(t, result.SatisfiedRefs, 2)
	require.NotEqual(t, coverageEvidenceFactKey(item), coverageEvidenceFactKey(otherField))
	require.NotEqual(t, coverageEvidenceFactKey(item), coverageEvidenceFactKey(otherVersion))
	require.NotEqual(t, coverageEvidenceFactKey(item), coverageEvidenceFactKey(otherUnit))
}

func TestA3MultiSourceTableAttributionChecksEveryCitedOperand(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	grid := 0
	const table = "Inventory"
	report := coverageEvidence{Kind: "body", SourceDomain: "knowledge", SourceID: "report", SourceTitle: "Report",
		Operation: agenttools.ToolReadDocument, ToolCallID: "read", ChunkID: "page", Page: 4, Sheet: table, Object: "grid", TableGrid: &grid,
		Raw: "Month\tClosing\tInbound\n2025-04\t57\t71\n2025-03\t31\t19\n2025-02\t4\t5"}
	book := coverageEvidence{Kind: "cell", SourceDomain: "structured", SourceID: "dataset", KnowledgeID: "workbook",
		CitationID: "ledger-ref", Metric: "Production", Raw: json.Number("24")}
	const cite = `<kb doc="Report" chunk_id="page" />`
	const other = `Warehouse Production was 24 <evidence evidence_id="ledger-ref" />.`
	review := coverageDraftReview{record: coverageRecord{Evidence: []coverageEvidence{report, book}}}
	for _, planned := range []bool{false, true} {
		if planned {
			review.comparisons = []coverageComparisonCheck{{GoalIndex: 0, State: coverageSatisfied, Table: table}}
		}
		missing, multisource := multiSourceTableAttribution(review, "Page 4 of Inventory for 2025-04 lists Closing at 57 "+cite+".\n\n"+other)
		require.True(t, multisource)
		require.Empty(t, missing, "independent physically grounded sources are legitimate")
		missing, multisource = multiSourceTableAttribution(review, "Inventory for 2025-04 lists Closing at 57 and Inbound at 19 "+cite+".\n\n"+other)
		require.True(t, multisource)
		require.Equal(t, []unattributedTableValue{{Source: "Report", Page: 4}}, missing)
		missing, _ = multiSourceTableAttribution(review, "Inventory for 2025-04 lists Closing at 57 "+cite+".")
		require.Empty(t, missing, "an unreferenced tool result does not create a cross-source claim")
	}
	book.KnowledgeID = report.SourceID
	review.record.Evidence[1] = book
	missing, multisource := multiSourceTableAttribution(review, "Inventory for 2025-04 lists Closing at 57 and Inbound at 19 "+cite+".\n\n"+other)
	require.False(t, multisource)
	require.Empty(t, missing, "two access tools for the same physical source cannot form two sides")
}

func TestA3FallbackKeepsDistinctReceiptsWithSameDisplayText(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(strconv.FormatBool(strict), func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", strconv.FormatBool(strict))
			first := coverageEvidence{Kind: "cell", SourceDomain: "structured", SourceID: "first", SourceTitle: "Stock report",
				SnapshotHash: "native-first", Sheet: "Inventory", Metric: "Closing", Column: "B", Cell: "B11",
				Date: "2025-03-11", Raw: json.Number("25"), Unit: "kg", UnitBasis: "native_header"}
			second := first
			second.SourceID, second.SnapshotHash = "second", "native-second"
			d := terminalCoverageDelivery{facts: []terminalCoverageFact{
				{coverageFact{"scoped_value", first}, true}, {coverageFact{"scoped_value", second}, true},
			}}
			answer := d.renderFallback(terminalTableEvidence)
			if strict {
				require.Equal(t, 2, strings.Count(answer, "value cell B11"))
				require.Contains(t, answer, "checked source 1")
				require.Contains(t, answer, "checked source 2")
			} else {
				require.Equal(t, 1, strings.Count(answer, "value cell B11"))
			}
			second = first
			first.Dimensions, second.Dimensions = map[string]any{"region": "North"}, map[string]any{"region": "South"}
			d.facts = []terminalCoverageFact{{coverageFact{"scoped_value", first}, true}, {coverageFact{"scoped_value", second}, true}}
			answer = d.renderFallback(terminalTableEvidence)
			if strict {
				require.Contains(t, answer, "region North")
				require.Contains(t, answer, "region South")
				require.Equal(t, 2, strings.Count(answer, "value cell B11"))
			} else {
				require.Equal(t, 1, strings.Count(answer, "value cell B11"))
			}
		})
	}
}

func TestA3FallbackRetainsIndependentlyCitedOriginalField(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	item := coverageEvidence{Kind: "cell", SourceDomain: "structured", SourceID: "workbook", SourceTitle: "Inventory",
		Metric: "Closing balance", Cell: "B11", Date: "2025-03-11", Raw: json.Number("25"), CitationID: "row-ref"}
	peer := item
	peer.Metric, peer.Cell = "Outbound", "C11" // The same amount in another field and the same aggregate citation.
	d := terminalCoverageDelivery{facts: []terminalCoverageFact{
		{coverageFact{"scoped_value", item}, false}, {coverageFact{"scoped_value", peer}, false},
	}}
	original := slices.Clone(d.facts)
	require.Equal(t, "no_model_draft", d.retainCitedDraftReadings(""))
	require.Equal(t, original, d.facts)
	require.Equal(t, "current_cited_reading", d.retainCitedDraftReadings(
		`The report is unverified. Inventory closing balance on 2025-03-11 was 25 (B11) <evidence evidence_id="row-ref" />.`))
	require.Len(t, d.facts, 1)
	require.Equal(t, "B11", d.facts[0].Evidence.Cell)
}
