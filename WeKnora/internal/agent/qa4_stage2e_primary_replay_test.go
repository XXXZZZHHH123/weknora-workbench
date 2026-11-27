package agent

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

// s2-4 had a rejected plan and only field metadata before it read the report.
// The missing native value read and its on-wire association are the tested
// recovery variant, using an offline original-cell result for the same snapshot.
func TestQA4Stage2EKB16PrimaryReplay(t *testing.T) {
	for _, mode := range []string{"default", "unrepaired", "planned", "only", "false"} {
		t.Run(mode, func(t *testing.T) {
			enabled := mode != "false" && mode != "default"
			r := newQA4Stage2CReplay(t, "s2-4-kb16", enabled, true)
			if mode == "default" {
				require.NoError(t, os.Unsetenv("WEKNORA_AGENT_EVIDENCE_COMPLETION"))
			}
			if mode == "only" {
				r.trace.Question = "仅使用棉花数据库。" + r.trace.Question
				r.messages[1] = chat.Message{Role: "user", Content: r.trace.Question}
				r.engine.turnReview.Query = r.trace.Question
				r.engine.reviewConversation = conversationThroughLastHuman(r.messages)
				r.engine.turnReview.Conversation = r.engine.reviewConversation
				r.trace.Calls[len(r.trace.Calls)-1].Arguments["source_question"] = r.trace.Question
				r.setPlan(StructuredTaskGoal{
					SourceMode: "only", SourceTime: "observed_period", VersionTask: "single",
					PrimaryRef: "棉花数据库", RestrictionBasis: "仅使用棉花数据库", Metric: "郑棉期货收盘价",
					Period:   map[string]any{"grain": "month", "from": "2024-03-01", "to": "2024-03-31"},
					Required: []string{"scoped_value"}, OnVerifiedGap: []string{"report_gap", "last_valid_same_field"},
				})
				r.engine.taskPlan.SourceDiscovery.SourceDomains = []string{"structured"}
			}
			r.engine.config.StructuredResultReviewEnabled = false
			reads := qa4Stage2DCountReads()
			require.Equal(t, enabled, r.engine.evidenceCompletionEnabled())
			r.through(12)
			if mode == "only" {
				require.NotEmpty(t, r.engine.evidenceCompletion.nativeSelections[0], "the existing native field catalog verified the scoped dataset")
			} else {
				require.Nil(t, r.engine.taskPlan)
			}
			require.EqualValues(t, 2, reads.Load())
			require.False(t, slices.ContainsFunc(r.engine.evidenceCompletion.record.Evidence, func(item coverageEvidence) bool {
				return item.Kind == "cell" || item.Kind == "gap" || item.Kind == "last_valid"
			}))
			if !enabled {
				r.through(29)
				historical := qa4Stage2EKB16Draft(t, r)
				r.run(chatAnswer(historical))
				r.exhaust()
				require.Equal(t, r.engine.modelContext.DecodeOutputText(historical), r.answer())
				require.Contains(t, r.answer(), "16085")
				require.NotContains(t, r.answer(), "17110")
				require.Empty(t, r.engine.evidenceCompletionTargets())
				require.EqualValues(t, 2, reads.Load())
				return
			}
			if mode != "only" {
				r.model.discovery = discoveryReply("recover", "The actual request remains unresolved. Select the pertinent primary field from the authorized directory and read its original values for the requested scope.")
				r.run(chatAnswer("《棉花数据库.xlsx》的字段目录已经找到。"))
				require.False(t, r.state.IsComplete)
				require.NotEmpty(t, r.engine.evidenceCompletion.legacyGap)
				r.through(29)
				require.True(t, r.results[25].Result.Success)
				require.True(t, r.results[29].Result.Success)
			}
			if mode == "unrepaired" {
				r.run(chatAnswer(qa4Stage2EKB16Draft(t, r)))
				r.exhaust()
				require.True(t, r.state.IsComplete)
				require.Empty(t, r.state.FinalAnswer)
				require.Empty(t, r.finals)
				notice := r.engine.turnReview.TerminalReceipt.notice
				require.NotEmpty(t, notice)
				require.NotContains(t, notice, "17110", "the historical execution did not read this original cell")
				require.NotContains(t, notice, "B4705")
				require.Empty(t, r.engine.evidenceCompletionTargets(), "the historical recovery has no model field association")
				require.EqualValues(t, 2, reads.Load())
				return
			}
			declaration := qa4Stage2EKB16PrimaryDeclaration(t, r, mode == "only")
			notice := r.engine.evidenceCompletionNotice()
			require.Contains(t, notice, declaration["selection_ref"])

			latest := r.trace.Calls[len(r.trace.Calls)-1]
			latest.Arguments = maps.Clone(latest.Arguments)
			latest.Arguments[coverageArgument] = declaration
			models := r.model.callCount
			r.calls([]qa4Stage2Call{latest})
			require.Equal(t, models+1, r.model.callCount, "bind and read through the existing execution round")
			require.EqualValues(t, 3, reads.Load())
			require.Equal(t, "2023-09-22", r.results[101].ExecutionArgs()["start_date"])
			require.Equal(t, "2023-09-22", r.results[101].ExecutionArgs()["end_date"])
			require.Len(t, r.engine.evidenceCompletionTargets(), 1)
			// This new model response is separate from the actual 2d draft.
			primary := "《棉花数据库.xlsx》的郑棉期货收盘价（CF.CZC）在 2024-03-01 至 2024-03-31 无有效月内观测；所读同字段末日为 2023-09-22，原值 17110（B4705）。"
			draft := primary
			if mode != "only" {
				body := slices.IndexFunc(r.engine.evidenceCompletion.record.Evidence, func(item coverageEvidence) bool {
					return item.Kind == "body" && item.ChunkID == "replay-0331-slide-2-table"
				})
				require.GreaterOrEqual(t, body, 0, "actual authorized report read must return its body")
				item := r.engine.evidenceCompletion.record.Evidence[body]
				handle := r.engine.modelContext.CitableChunkHandle(item.ChunkID)
				require.NotEmpty(t, handle)
				draft += fmt.Sprintf("\n\n本次补充采用《棉花周报20240331.pptx》第2页的合约行情表。2024-03-29，CF2405 合约收盘为 16085，原表未标明单位<ref id=\"%s\"/>。", handle)
				original := item.Raw.(string)
				const row = "2024-03-29 | 16085"
				rowIndex := strings.Index(original, row)
				require.Positive(t, rowIndex)
				span := original[:rowIndex+len(row)]
				require.True(t, strings.HasPrefix(span, "CF2405 | CF2409 | CF2501"))
				claim := map[string]any{
					"goal_index": 0, "selection_ref": coverageObservationRef(item), "role": "supplement",
					"period": "2024-03-29", "grain": "day", "date": "2024-03-29",
					"span": span, "subject": "CF2405", "value_text": "16085",
				}
				wrongColumn := maps.Clone(claim)
				wrongColumn["subject"] = "CF2409"
				for _, selectedSpan := range []string{span, span[strings.Index(span, "CF2409"):]} {
					wrongColumn["span"] = selectedSpan
					raw, err := json.Marshal(wrongColumn)
					require.NoError(t, err)
					_, valid := r.engine.acceptCoverageDeclaration(r.ctx, r.trace.Question, r.messages, raw)
					require.False(t, valid, "the same date and number cannot be assigned to a different contract")
				}
				draft = r.withDeclaration(draft, claim)
			}
			r.run(chatAnswer(draft))
			r.exhaust()
			answer := r.answer()
			require.Equal(t, r.engine.modelContext.DecodeOutputText(stripCoverageAnswer(draft)), answer)
			for _, want := range []string{"棉花数据库.xlsx", "2024-03-01", "2024-03-31", "2023-09-22", "17110", "B4705"} {
				require.Contains(t, answer, want)
			}
			require.Equal(t, 1, strings.Count(answer, "17110"))
			require.EqualValues(t, 3, reads.Load())
			if mode == "only" {
				require.Empty(t, r.state.KnowledgeRefs)
				require.Empty(t, r.index.searches)
				return
			}
			for _, want := range []string{"本次补充采用", "棉花周报20240331.pptx", "第2页", "2024-03-29", "CF2405", "16085", "原表未标明单位"} {
				require.Contains(t, answer, want)
			}
			require.Equal(t, 1, strings.Count(answer, "16085"))
			require.NotContains(t, answer, " | ", "the selected report claim is delivered in its cited prose")
			require.Less(t, strings.Index(answer, "17110"), strings.Index(answer, "16085"))
		})
	}
}

func qa4Stage2EKB16Draft(t *testing.T, r *qa4Stage2CReplay) string {
	t.Helper()
	draft := r.trace.Drafts[0]
	doc, chunkID := r.trace.Documents[0], "replay-0331-slide-2-table"
	handle := r.engine.modelContext.CitableChunkHandle(chunkID)
	require.NotEmpty(t, handle)
	tag := fmt.Sprintf(`<kb doc="%s" chunk_id="%s" kb_id="%s" />`, doc.Title, chunkID, qa4Stage2Library)
	require.Contains(t, draft, tag)
	return strings.Replace(draft, tag, fmt.Sprintf(`<ref id="%s"/>`, handle), 1)
}

func qa4Stage2EKB16PrimaryDeclaration(t *testing.T, r *qa4Stage2CReplay, only bool) map[string]any {
	t.Helper()
	index := slices.IndexFunc(r.engine.evidenceCompletion.record.Evidence, func(item coverageEvidence) bool {
		return item.Kind == "directory" && item.Operation == "list_series" && item.SeriesID == "futures.close.CF.CZC"
	})
	require.GreaterOrEqual(t, index, 0, "primary field must be visible to the executing model")
	item := r.engine.evidenceCompletion.record.Evidence[index]
	goal := map[string]any{
		"goal_index": 0, "request_basis": r.trace.Question, "source_mode": "named_first", "primary_ref": "棉花数据库",
		"metric": "郑棉期货收盘价", "source_time": "observed_period", "version_task": "single",
		"period":   map[string]any{"from": "2024-03-01", "to": "2024-03-31", "grain": "month"},
		"required": []string{"scoped_value"}, "on_verified_gap": []string{"report_gap", "last_valid_same_field", "attributed_supplement"},
		"delivery": map[string]any{"kind": "text"},
	}
	declaration := map[string]any{
		"goal_index": 0, "selection_ref": coverageObservationRef(item), "role": "primary",
		"period_from": "2024-03-01", "period_to": "2024-03-31", "grain": "month",
	}
	if !only {
		declaration["goal"], declaration["gap_id"] = goal, r.engine.evidenceCompletion.legacyGap
	}
	return declaration
}
