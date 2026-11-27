package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// The two no-data receipts are from s2-5 KB16 SSE #22 and #26. The saved
// no-tool candidate texts are from the same turn. IDs of the library and
// imported workbook were aliased and v2 hashes recalculated. Additional
// historical reads are outside this two-field replay.
func TestQA4StageA1RecordedGapsWithoutPlanOrDeclarations(t *testing.T) {
	for _, mode := range []struct {
		name    string
		badPlan bool
		reverse bool
	}{
		{name: "historical order"},
		{name: "invalid plan", badPlan: true},
		{name: "parallel return and candidate order reversed", badPlan: true, reverse: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			r := newQA4Stage2CReplay(t, "s2-5-kb16-gaps", false, false)
			if mode.badPlan {
				rejected := &qa4ReplayModel{t: t, plan: json.RawMessage(`{"goals":[],"evidence":null}`), counts: map[string]int{}}
				r.engine.chatModel = rejected
				r.engine.prepareTaskPlanning()
				r.messages = r.engine.planWorkbenchTask(r.ctx, r.state, r.trace.Question, r.messages, "bad-plan")
				require.Equal(t, 1, rejected.counts["agent_task_plan"])
				r.engine.chatModel = r.model
			}
			require.Nil(t, r.engine.taskPlan)
			r.engine.config.StructuredResultReviewEnabled = false
			calls := slices.Clone(r.trace.Calls)
			if mode.reverse {
				// Simulate both same-domain receipts returning in the
				// opposite order in a parallel batch, with identical inputs.
				slices.Reverse(calls)
				r.calls(calls)
			} else {
				r.through(26)
			}
			require.Len(t, r.results, 2)
			for _, number := range []int{22, 26} {
				call := r.results[number]
				require.True(t, call.Result.Success)
				require.Zero(t, citationInt(call.Result.Data["valid_count"]))
				require.Equal(t, "no_data", call.Result.Data["status"])
				require.Empty(t, call.Result.Data["rows"])
				require.Equal(t, "2024-03-01", call.ExecutionArgs()["start_date"])
				require.Equal(t, "2024-03-31", call.ExecutionArgs()["end_date"])
			}
			require.NotEqual(t, r.results[22].ExecutionArgs()["series_id"], r.results[26].ExecutionArgs()["series_id"])
			drafts := slices.Clone(r.trace.Drafts)
			if mode.reverse {
				slices.Reverse(drafts)
			}
			for _, draft := range drafts {
				decision := r.engine.checkCandidate(r.ctx, r.trace.Question, draft, "", r.state, []chat.Message{{Role: "user", Content: r.trace.Question}}, false)
				require.NoError(t, decision.Err)
				require.Nil(t, decision.CoverageFeedback, "a rejected or absent plan cannot invent a required role")
			}
			require.Empty(t, r.engine.evidenceCompletionTargets())
			r.exhaust()
			answer := r.answer()
			for _, want := range []string{"棉花数据库.xlsx", "郑棉期货", "收盘价", "2024-03-01", "2024-03-31", "无有效记录"} {
				require.Contains(t, answer, want)
			}
			require.NotContains(t, answer, "2023-09-22", "neither response read the historical endpoint")
			require.NotContains(t, answer, "17110")
			require.NotContains(t, answer, "B4705")
			require.False(t, terminalCandidateFallback(answer))
		})
	}
}

func TestQA4StageA1RanksCitedNativeFieldsWithoutDeclaration(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(strconv.FormatBool(reverse), func(t *testing.T) {
			r := newQA4Stage2CReplay(t, "s2-2-kb07", false, false)
			r.through(15)
			r.engine.config.StructuredResultReviewEnabled = false
			require.Empty(t, r.engine.evidenceCompletionTargets())
			fields := map[string]coverageEvidence{}
			for _, item := range r.engine.evidenceCompletion.record.Evidence {

				if item.Kind == "cell" && item.CitationID != "" {
					fields[item.Cell] = item
				}
			}
			claim := func(cell, value string) string {
				item, ok := fields[cell]
				require.True(t, ok)
				handle := r.engine.modelContext.CitableEvidenceHandle(item.CitationID)
				require.NotEmpty(t, handle)
				return r.engine.modelContext.DecodeOutputText(fmt.Sprintf("棉花数据库的 %s 在 %s 原值为 %s（%s）<ref id=%q/>。", item.Metric, item.Date, value, item.Cell, handle))
			}
			one := claim("B87", "231.93")
			two := claim("C87", "110") + "\n\n" + claim("G87", "298.93")
			drafts := []string{one, two}
			if reverse {
				slices.Reverse(drafts)
			}
			for _, draft := range drafts {
				decision := r.engine.checkCandidate(r.ctx, r.trace.Question, draft, "", r.state, r.messages, false)
				require.Equal(t, candidateAccepted, decision.Disposition)
				require.Nil(t, decision.CoverageFeedback, "current cited values do not invent a target")
			}
			require.Len(t, r.engine.turnReview.CoverageDraftRoles, 2)
			r.exhaust()
			if types.VerifiedPublicationEnabled() {
				r.engine.emitCompletionEvent(r.ctx, r.state, "s2c", "turn", time.Now())
			}
			answer := r.answer()
			if types.VerifiedPublicationEnabled() {
				require.Contains(t, answer, "产量 在 2023-10-31 原值为 110（C87）")
				require.Contains(t, answer, "工商业期末库存 在 2023-10-31 原值为 298.93（G87）")
				require.Equal(t, 2, strings.Count(answer, "〔棉花数据库.xlsx〕"))
				require.NotContains(t, answer, "<evidence")
				require.NotContains(t, answer, "B87")
			} else {
				require.Equal(t, two, answer)
			}
		})
	}
}
