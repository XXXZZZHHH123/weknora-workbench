package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestNumericUnverifiedFinalSynthesisKeepsLastDraftAfterTwoRepairs(t *testing.T) {
	for _, mode := range []string{"standard", "strict"} {
		t.Run(mode, func(t *testing.T) {
			model := &mockChat{responses: []mockResponse{
				chatAnswer("初稿：138.67，附加999。"),
				chatAnswer("修订：138.67，附加998。"),
				chatAnswer("末稿：138.67，附加997。"),
			}}
			e := numericRecoveryTestEngine(t, model)
			e.config.AnswerReviewMode = mode
			// Legacy callers without a source critic also reach numeric failure;
			// they receive the same terminal presentation, never numeric approval.
			if mode == "strict" {
				e.config.TaskPlanningEnabled = false
			}
			state, messages, query := numericRecoveryFixture()
			require.NoError(t, e.streamFinalAnswerToEventBus(t.Context(), query, state, "numeric", messages))
			require.Len(t, model.calls, 3)
			require.Equal(t, 2, e.turnReview.Repairs)
			require.Equal(t, 3, e.turnReview.Checks)
			require.True(t, strings.HasPrefix(state.FinalAnswer, "末稿：138.67，附加997〔未核验〕。"))
			require.Contains(t, state.FinalAnswer, "请以引用的原表、原文或数据为准")
			require.NotContains(t, state.FinalAnswer, "999")
			require.False(t, e.candidateApprovalCurrent(state.FinalAnswer, state))
			require.Equal(t, 1, e.turnReview.NumericUnverifiedDelivery.Marked)
		})
	}
}

func TestNumericUnverifiedKeepsSourceNamesCodesAndDatesIntact(t *testing.T) {
	const fileName = "棉花周报20240310-冲突测试版.pptx"
	const titleWithoutExtension = "棉花周报20240310-冲突测试版"
	const candidate = "《" + fileName + "》（" + titleWithoutExtension + "）的日期是2024 年 3 月 10 日。" +
		"合约CF05M，系列futures.close.CF05M.CZC。最高138.67万元，另有汇总20240310元，增幅18.25%。"
	const expected = "《" + fileName + "》（" + titleWithoutExtension + "）的日期是2024 年 3 月 10 日。" +
		"合约CF05M，系列futures.close.CF05M.CZC。最高138.67万元，另有汇总20240310〔未核验〕元，增幅18.25%〔未核验〕。"
	e := numericRecoveryTestEngine(t, &mockChat{})
	e.config.AnswerReviewMode = "standard"
	state, messages, query := numericRecoveryFixture()
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{
		{Name: "search_knowledge", Result: &types.ToolResult{Success: true, Data: map[string]any{
			"display_type": "document_info", "documents": []map[string]any{{
				"knowledge_id": "report", "title": fileName, "file_name": fileName,
				"metadata": map[string]any{"report_date": "2024-03-10"},
			}},
		}}},
		{Name: "structured_list_series", Result: &types.ToolResult{Success: true,
			Output: `{"status":"ok","series":[{"series_id":"futures.close.CF05M.CZC","dimensions":{"contract":"CF05M.CZC"}}]}`}},
	}})
	e.finishStalledTurn(t.Context(), state, "numeric", &types.ChatResponse{Content: candidate}, false, query, messages)
	require.Equal(t, expected+"\n\n> 提示：标了〔未核验〕的数字没能和本轮查到的原文或数据逐项对上，请以引用的原表、原文或数据为准。", state.FinalAnswer)
	require.Equal(t, []string{"20240310", "18.25"}, e.turnReview.Last.Unsupported)
	require.Equal(t, 2, e.turnReview.NumericUnverifiedDelivery.Marked)
}

func TestNumericUnverifiedOrdinaryStopRepairsBeforeDelivery(t *testing.T) {
	const candidate = "The measured value is 138.67; optional total 999."
	model := &mockChat{responses: []mockResponse{chatAnswer(candidate), chatAnswer(candidate), chatAnswer(candidate)}}
	e := numericRecoveryTestEngine(t, model)
	e.config.AnswerReviewMode = "standard"
	state, messages, query := numericRecoveryFixture()
	recorder := &terminalAnswerRecorder{}
	recorder.attach(e.eventBus)
	_, err := e.executeLoop(t.Context(), state, query, messages, e.buildToolsForLLM(), "numeric", "message")
	require.NoError(t, err)
	require.Len(t, model.calls, 3)
	require.Equal(t, 2, e.turnReview.Repairs)
	require.Contains(t, state.FinalAnswer, "999〔未核验〕")
	done, visible := recorder.snapshot()
	require.Equal(t, 1, done)
	require.Equal(t, state.FinalAnswer, visible)
}

func TestNumericUnverifiedStalledStopHasNoRepairRoundLeft(t *testing.T) {
	e := numericRecoveryTestEngine(t, &mockChat{})
	e.config.AnswerReviewMode = "standard"
	state, messages, query := numericRecoveryFixture()
	e.finishStalledTurn(t.Context(), state, "numeric", &types.ChatResponse{Content: "Measured value 138.67; optional total 999."}, false, query, messages)
	require.Contains(t, state.FinalAnswer, "999〔未核验〕")
	require.Equal(t, 1, e.turnReview.Checks)
	require.Zero(t, e.turnReview.Repairs)
}

func TestNumericUnverifiedPreservesOnlyUnmatchedOccurrencesAndReferences(t *testing.T) {
	for _, envelope := range []bool{false, true} {
		t.Run(map[bool]string{false: "prose", true: "envelope"}[envelope], func(t *testing.T) {
			e, state := citationDeliveryFixture(t, &mockChat{}, true)
			e.config.AnswerReviewMode = "standard"
			numeric, _, _ := numericRecoveryFixture()
			numeric.RoundSteps[0].ToolCalls[0].Result.Data["values"] = []any{map[string]any{"value": 0.43}}
			raw, err := json.Marshal(numeric.RoundSteps[0].ToolCalls[0].Result.Data)
			require.NoError(t, err)
			numeric.RoundSteps[0].ToolCalls[0].Result.Output = string(raw)
			state.RoundSteps = append(state.RoundSteps, numeric.RoundSteps...)
			const citation = `<kb chunk_id="current-b" />`
			base := "比例43%，数值43；另一个数值43。" + citation + "\n\n| 项 | 数值 |\n|---|---|\n| A | 43 |"
			candidate := base
			if envelope {
				encoded, err := json.Marshal(map[string]string{"answer": base})
				require.NoError(t, err)
				candidate = string(encoded)
			}
			d := e.checkCandidate(t.Context(), "Report the actual values.", candidate, "", state, nil, false)
			require.Equal(t, candidateNumericFailure, d.Disposition)
			e.commitTerminalResult(t.Context(), state, "numeric", numericFailureFallback, "", "failed", false)
			require.True(t, strings.HasPrefix(state.FinalAnswer, "比例43%，数值43〔未核验〕；另一个数值43〔未核验〕。"+citation))
			require.Contains(t, state.FinalAnswer, "| A | 43〔未核验〕 |")
			require.Equal(t, 3, e.turnReview.NumericUnverifiedDelivery.Marked)
			e.emitCompletionEvent(t.Context(), state, "numeric", "message", time.Now())
			require.Len(t, state.KnowledgeRefs, 1)
			require.Equal(t, "current-b", state.KnowledgeRefs[0].ID)
			require.Contains(t, state.FinalAnswer, citation)
		})
	}
}

func TestNumericUnverifiedStillRefusesInvalidOrStaleCandidate(t *testing.T) {
	for _, mutation := range []string{"citation", "malformed citation", "task", "evidence", "scope", "candidate", "code"} {
		t.Run(mutation, func(t *testing.T) {
			e := numericRecoveryTestEngine(t, &mockChat{})
			e.config.AnswerReviewMode = "standard"
			state, messages, query := numericRecoveryFixture()
			candidate := "The measured value is 138.67; optional total 999."
			switch mutation {
			case "citation":
				candidate += `<kb chunk_id="unknown" />`
			case "malformed citation":
				candidate += `<kb chunk_id="unknown"`
			case "code":
				candidate = "The measured value is 138.67; `999`."
			}
			e.checkCandidate(t.Context(), query, candidate, "", state, messages, false)
			switch mutation {
			case "task":
				e.reviewConversationReady = true
				e.reviewConversation = []chat.Message{{Role: "user", Content: "A changed task."}}
			case "evidence":
				state.RoundSteps[0].ToolCalls[0].Result.Data["changed"] = true
			case "scope":
				e.config.KnowledgeIDs = []string{"changed-scope"}
			case "candidate":
				e.turnReview.NumericUnverifiedDecision.Candidate += " replacement"
			}
			e.commitTerminalResult(t.Context(), state, "numeric", numericFailureFallback, "", "failed", false)
			require.Equal(t, numericFailureFallback, state.FinalAnswer)
		})
	}
}

func TestNumericUnverifiedEarlierGroundedCandidateStillWins(t *testing.T) {
	for _, warning := range []bool{false, true} {
		t.Run(map[bool]string{false: "grounded", true: "numeric warning"}[warning], func(t *testing.T) {
			e := numericRecoveryTestEngine(t, &mockChat{})
			e.config.AnswerReviewMode = "standard"
			state, messages, query := numericRecoveryFixture()
			e.checkCandidate(t.Context(), query, "Measured value 138.67; optional total 999.", "", state, messages, false)
			d := e.turnReview.NumericUnverifiedDecision
			d.Candidate = "Earlier checked core result."
			d.CandidateHash = candidateTextDigest(d.Candidate)
			d.Unsupported = nil
			d.SourceReviewAttempted = true
			d.Disposition = candidateRevise
			if warning {
				d.Disposition = candidateNumericWarning
				d.Unsupported = []string{"998"}
			}
			e.rememberUnverifiedCandidate(d.Candidate, d)
			e.commitTerminalResult(t.Context(), state, "numeric", finalNumericFailureFallback, "", "failed", false)
			require.True(t, strings.HasPrefix(state.FinalAnswer, "Earlier checked core result."))
			require.NotContains(t, state.FinalAnswer, "999")
			require.Nil(t, e.turnReview.NumericUnverifiedDelivery)
		})
	}
}

func TestNumericUnverifiedStrictCannotReuseInvalidCitation(t *testing.T) {
	e := numericRecoveryTestEngine(t, &mockChat{})
	state, messages, query := numericRecoveryFixture()
	d := e.checkCandidate(t.Context(), query, `Measured value 138.67; optional total 999.<kb chunk_id="unknown" />`, "", state, messages, false)
	require.Equal(t, candidateNumericFailure, d.Disposition)
	require.Empty(t, e.turnReview.NumericUnverifiedDecision.Candidate)
	e.commitTerminalResult(t.Context(), state, "numeric", finalNumericFailureFallback, "", "failed", false)
	require.Equal(t, finalNumericFailureFallback, state.FinalAnswer)
}

func TestNumericUnverifiedCompletionKeepsOnlyCurrentEvidenceNavigation(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "changed"}[changed], func(t *testing.T) {
			e, state := rawCitationFixture(t)
			e.config.AnswerReviewMode = "standard"
			e.currentStructuredCitationView(state.RoundSteps[0].ToolCalls[0])
			id, _ := selectRawCitation(t, e, 1, 1)
			base := `Measured value 12.500 kg; optional total 999.<evidence evidence_id="` + id + `" />`
			d := e.checkCandidate(t.Context(), "Report the observations.", base, "", state, nil, false)
			require.Equal(t, candidateNumericFailure, d.Disposition)
			e.commitTerminalResult(t.Context(), state, "numeric", finalNumericFailureFallback, "", "failed", false)
			require.Contains(t, state.FinalAnswer, "999〔未核验〕")
			if changed {
				state.RoundSteps[0].ToolCalls[0].Result.Data["changed"] = true
			}
			e.emitCompletionEvent(t.Context(), state, "numeric", "message", time.Now())
			if changed {
				require.Equal(t, numericFailureFallback, state.FinalAnswer)
				require.Empty(t, state.EvidenceRefs)
			} else {
				require.Len(t, state.EvidenceRefs, 1)
				require.Equal(t, "unverified", state.EvidenceRefs[0].ReviewStatus)
				require.Equal(t, candidateTextDigest(state.FinalAnswer), state.EvidenceRefs[0].CandidateHash)
				require.False(t, e.candidateApprovalCurrent(state.FinalAnswer, state))
			}
		})
	}
}
