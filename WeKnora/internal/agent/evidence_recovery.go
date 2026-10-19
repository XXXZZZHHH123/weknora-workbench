package agent

import (
	"context"
	"encoding/json"

	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

const reviewCollectEvidence = "collect_evidence"
const reviewReviseAnswer = "revise_answer"
const maxEvidenceRecoveryAttempts = 2

// The evaluator chooses the disposition from the full task and actual evidence.
// The runtime only bounds recovery and restores the authorized tool loop. It
// never interprets feedback words or turns the evaluator's advice into facts.
func (e *AgentEngine) resumeEvidenceCollection(ctx context.Context, state *types.AgentState, messages *[]chat.Message, guards *loopGuards, sessionID string) {
	guards.evidenceRecoveryAttempts++
	restored, restoredOriginal := restoreStructuredReviewConversation(*messages, guards)
	*messages = restored
	*messages = append(*messages, chat.Message{Role: "system", Content: evidenceRecoveryInstructions + e.answerRepairAdvice()})
	if handoff, ok := reviewEvidenceHandoffMessage(e.turnReview.ReadEvidence); ok {
		*messages = append(*messages, handoff)
	}
	e.turnReview.ReadEvidence = nil
	e.structuredReviewMode = false      // Resume the user's actual reasoning settings.
	e.clearTaskToolProjection(messages) // Recovery may need a capability omitted by the initial plan.
	guards.structuredAnswerReviewed = false
	guards.structuredReviewEvidence = nil
	guards.structuredReviewInput = ""
	guards.sawPlainRound = false
	guards.consecutiveSameContent = 0
	e.contextRewrites++
	e.lastUsage = types.TokenUsage{}
	e.lastSentMsgCount = 0
	e.emitProgress(ctx, "sources", state.CurrentRound, sessionID)
	common.PipelineInfo(ctx, "Agent", "evidence_recovery", map[string]interface{}{
		"iteration": state.CurrentRound, "attempt": guards.evidenceRecoveryAttempts,
		"disposition":                  reviewCollectEvidence,
		"phase":                        "evidence_collection",
		"incomplete_answer_retries":    guards.incompleteAnswerRetries,
		"original_transcript_restored": restoredOriginal,
	})
}

// restoreStructuredReviewConversation removes only the exact synthetic review
// envelope recorded by the runtime. The original human transcript is restored
// and the live tail is preserved; a changed/missing envelope leaves the live
// transcript untouched. Always return a fresh slice so callers cannot mutate
// the input through append.
func restoreStructuredReviewConversation(messages []chat.Message, guards *loopGuards) ([]chat.Message, bool) {
	if guards == nil || len(guards.structuredReviewEvidence) == 0 || guards.structuredReviewInput == "" || len(messages) < 2 {
		return append([]chat.Message(nil), messages...), false
	}
	baseSystem := ""
	if guards.structuredReviewEvidence[0].Role == "system" {
		baseSystem = guards.structuredReviewEvidence[0].Content
	}
	expectedReviewSystem := structuredReviewSystemContent(baseSystem)
	for i := 0; i+1 < len(messages); i++ {
		if messages[i].Role != "system" || messages[i].Content != expectedReviewSystem ||
			messages[i+1].Role != "user" || messages[i+1].Content != guards.structuredReviewInput {
			continue
		}

		// Keep any live prelude (including trusted system messages). Avoid
		// duplicating the saved transcript's leading system message when the
		// prelude already contains that exact message.
		prefix := append([]chat.Message(nil), messages[:i]...)
		evidence := append([]chat.Message(nil), guards.structuredReviewEvidence...)
		if len(evidence) > 0 && evidence[0].Role == "system" {
			for _, message := range prefix {
				if message.Role == evidence[0].Role && message.Content == evidence[0].Content &&
					message.Name == evidence[0].Name && message.ToolCallID == evidence[0].ToolCallID {
					evidence = evidence[1:]
					break
				}
			}
		}
		restored := make([]chat.Message, 0, len(prefix)+len(evidence)+len(messages)-i-2)
		restored = append(restored, prefix...)
		restored = append(restored, evidence...)
		restored = append(restored, messages[i+2:]...)
		return restored, true
	}
	return append([]chat.Message(nil), messages...), false
}

const evidenceRecoveryInstructions = `内部运行状态：审核提出了待核实的证据缺口，现返回证据收集阶段。先核对已有成功结果及已补读片段；审核意见不代表原证据缺失或失效。后续失败仅约束该次请求，不抹去先前成功返回，仍按实际来源范围判断。根据完整用户请求、真实来源及下面的核验意见，自行决定最小必要的读取或计算计划并调用实际工具；不要仅把缺失结论删掉就宣布完成，也不要把核验意见当成来源事实。
已有证据继续复用。结果被截断、分页未读完或仅有目录/检索摘要时，利用真实来源ID、页码、范围和续读位置定向补读；多来源任务分别核实必要来源，不用一个文件的片段替其他文件。读取顺序、口径和工具由你根据任务决定，权限、来源绑定和确定性计算规则仍适用。新取得证据后重新形成完整候选并接受核验。真正无法取得时说明已尝试的限制和可回答范围，不编造原值、不声称全部完成。` + "\n" + structuredAvailabilityInstructions

const numericValidationRepairInstructions = `内部运行状态：确定性数值校验指出当前候选含有尚未匹配到真实程序结果的数字。以下校验记录只是运行时反馈，不是来源事实。依据完整用户请求、真实工具结果和该反馈修订：保留用户必答内容；可以删除非必要且无依据的附加数字，也可以自行选择已授权的读取或计算工具补足依据。不要把校验反馈当作来源，不要编造、改写或放宽数值规则；修改后的候选仍会再次接受来源核验和确定性数值校验。`

// resumeNumericValidationRepair restores the exact pre-review transcript and
// resumes the ordinary authorized tool loop. Numeric repairs have their own
// caller-owned bound and do not consume or reset source-recovery attempts.
func (e *AgentEngine) resumeNumericValidationRepair(ctx context.Context, state *types.AgentState, messages *[]chat.Message, guards *loopGuards, sessionID, candidate string, feedback map[string]any) {
	restored, restoredOriginal := restoreStructuredReviewConversation(*messages, guards)
	if len(guards.numericValidationInputs) > 0 {
		prior := make(map[string]bool, len(guards.numericValidationInputs))
		for _, input := range guards.numericValidationInputs {
			prior[input] = true
		}
		filtered := make([]chat.Message, 0, len(restored))
		for _, message := range restored {
			if message.Role == "system" && prior[message.Content] {
				continue
			}
			filtered = append(filtered, message)
		}
		restored = filtered
	}
	*messages = restored
	encodedFeedback, err := json.Marshal(feedback)
	if err != nil {
		// The builder emits JSON-safe protocol values. Keep a fail-closed
		// representation if a future field violates that contract.
		encodedFeedback = []byte(`{"status":"feedback_encoding_failed"}`)
	}
	feedbackMessage := numericValidationRepairInstructions + "\n数值校验记录：" + string(encodedFeedback)
	guards.numericValidationInputs = append(guards.numericValidationInputs, feedbackMessage)
	*messages = append(*messages, chat.Message{Role: "assistant", Content: candidate}, chat.Message{Role: "system", Content: feedbackMessage})
	e.structuredReviewMode = false
	e.clearTaskToolProjection(messages)
	guards.sawPlainRound = false
	guards.consecutiveSameContent = 0
	e.contextRewrites++
	e.lastUsage = types.TokenUsage{}
	e.lastSentMsgCount = 0
	e.emitProgress(ctx, "sources", state.CurrentRound, sessionID)
	common.PipelineInfo(ctx, "Agent", "numeric_validation_recovery", map[string]interface{}{
		"iteration": state.CurrentRound, "attempt": guards.structuredEvidenceRepairs,
		"phase": "numeric_candidate_repair", "original_transcript_restored": restoredOriginal,
	})
}
