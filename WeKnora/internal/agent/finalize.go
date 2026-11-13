package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/agent/compaction"
	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

type finalAnswerOptions struct{ preliminary bool }

// streamFinalAnswerToEventBus streams the final answer generation through EventBus
func (e *AgentEngine) streamFinalAnswerToEventBus(
	ctx context.Context,
	query string,
	state *types.AgentState,
	sessionID string,
	conversation []chat.Message,
	options ...finalAnswerOptions,
) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	ctx, cancelFinalBudget := e.standardAnswerFinalContext(ctx, state)
	defer cancelFinalBudget()
	if hasWorkspaceExecution(state) {
		e.recordArtifactInventory(ctx, state, &conversation, sessionID)
	}
	totalToolCalls := countTotalToolCalls(state.RoundSteps)
	logger.Infof(ctx, "[Agent][FinalAnswer] Synthesizing from %d steps, %d tool calls",
		len(state.RoundSteps), totalToolCalls)
	common.PipelineInfo(ctx, "Agent", "final_answer_start", map[string]interface{}{
		"session_id":   sessionID,
		"query":        query,
		"steps":        len(state.RoundSteps),
		"tool_results": totalToolCalls,
	})

	// Reuse the live transcript, including history, images, compaction and steer
	// messages. Tool output must retain its role and call ID; never promote it
	// into user instructions during error recovery or iteration-limit synthesis.
	messages := append([]chat.Message(nil), conversation...)
	reviewedDelivery := e.structuredReviewMode
	if reviewedDelivery {
		// Exceptional final synthesis uses the same reviewed-answer protocol as
		// ordinary review rounds. Keep the trusted schema instruction visible to
		// providers that treat Format as JSON-object mode rather than enforcement.
		messages = withReviewedAnswerDeliveryInstruction(messages)
	}
	runtimeNotice := "<runtime_event>{\"source\":\"runtime\",\"type\":\"final_synthesis\",\"message\":\"Tool " +
		"execution has ended for this run. Respond to the current task, including the " +
		"latest user corrections and source restrictions in the conversation. Base " +
		"claims on the evidence actually obtained; distinguish completed work from " +
		"remaining work and explain any missing evidence. Use the user's requested " +
		"language and format. Do not claim that an unperformed action " +
		"succeeded.\"}</runtime_event>"
	preliminary := len(options) > 0 && options[0].preliminary
	if preliminary {
		runtimeNotice += "\n" + standardAnswerBudgetNotice
	}
	if _, bounded := ctx.Value(standardAnswerFinalDeadlineKey{}).(time.Time); bounded && e.standardPreliminaryProse() {
		runtimeNotice += "\nGive a concise, useful answer focused on the supported core findings. State uncovered scope briefly; omit optional item-by-item analysis."
	}
	if e.finalAnswerNeedsCompaction(messages, runtimeNotice) {
		messages, _ = e.compactFinalAnswerContext(ctx, messages, state.CurrentRound+1,
			runtimeNotice, compaction.ReasonThreshold)
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}
	fullAnswer := ""
	outcome := "failed"
	truncated := false
	overflowRetried := false
	// Synthesis is an exceptional final path (iteration exhaustion or API
	// recovery). Buffer it until verified, so malformed/unsupported drafts do
	// not become the visible terminal answer while a repair is still running.
	for attempt := 0; attempt < 3; attempt++ {
		e.emitProgress(ctx, "generation", state.CurrentRound, sessionID)
		inputTokens := e.finalAnswerInputTokens(messages, runtimeNotice)
		budget := e.clampCompletionBudgetToContext(inputTokens)
		if budget > 4096 {
			budget = 4096
		}
		if e.config.MaxContextTokens > 0 && inputTokens+budget > e.config.MaxContextTokens {
			return fmt.Errorf("final answer synthesis cannot fit context window after compaction: input %d + completion %d > context %d", inputTokens, budget, e.config.MaxContextTokens)
		}
		disabled := false
		reasoning := chat.ReasoningEffort("off")
		if e.config.ReviewReasoningEffort != "" {
			reasoning = chat.ReasoningEffort(e.config.ReviewReasoningEffort)
		}
		opts := &chat.ChatOptions{
			RuntimeNotice: runtimeNotice,
			Temperature:   e.config.Temperature, MaxCompletionTokens: budget,
			PromptCacheKey: sessionID, ToolChoice: "none", Thinking: &disabled,
			ReasoningEffort: reasoning, ThinkingBudgetTokens: 0,
		}
		if reviewedDelivery {
			// Iteration exhaustion and API recovery retain the same explicit
			// delivery contract as ordinary review rounds. Otherwise escaped
			// citations would be decoded inside still-serialized JSON, or the
			// answer envelope itself could become the terminal user response.
			opts.Temperature = 0
			opts.Format = reviewedAnswerSchema
		}
		if _, bounded := ctx.Value(standardAnswerFinalDeadlineKey{}).(time.Time); bounded {
			deadline, _ := ctx.Deadline()
			common.PipelineInfo(ctx, "Agent", "standard_answer_model_budget", map[string]any{
				"purpose": "source_final", "attempt": attempt + 1, "remaining_ms": max(time.Until(deadline).Milliseconds(), 0),
				"completion_tokens": opts.MaxCompletionTokens,
			})
		}
		llmResult, err := e.streamLLMToEventBus(ctx, messages, opts, nil)
		if llmResult != nil && llmResult.Usage != nil {
			state.TurnUsage.Accumulate(*llmResult.Usage)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		overflow := compaction.IsOverflowError(err)
		if llmResult != nil && llmResult.Usage != nil && err == nil && e.responseHitContextLimit(&types.ChatResponse{FinishReason: llmResult.FinishReason, Usage: *llmResult.Usage}) {
			overflow = true
			err = fmt.Errorf("final answer synthesis reached the context window")
		}
		if overflow && !overflowRetried {
			overflowRetried = true
			compacted, changed := e.compactFinalAnswerContext(ctx, messages, state.CurrentRound+1, runtimeNotice, compaction.ReasonOverflow)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if changed {
				messages = compacted
				attempt--
				continue
			}
		}
		if err != nil {
			return err
		}
		fullAnswer = agenttools.StripThinkBlocks(llmResult.Content)
		if isReviewedAnswerFormat(opts.Format) {
			fullAnswer = agenttools.StripThinkBlocks(decodeReviewedAnswer(llmResult.Content))
			fullAnswer = e.modelContext.DecodeOutputText(fullAnswer)
		}
		truncated = isLengthFinishReason(llmResult.FinishReason)
		// A provider can ignore tool_choice=none. Never accept its pre-tool
		// progress text as the final answer, or silently drop requested calls.
		invalid := strings.TrimSpace(fullAnswer) == "" || llmResult.StreamError != "" || len(llmResult.ToolCalls) > 0 || !isNaturalStopFinishReason(llmResult.FinishReason)
		unsupported := []string{}
		if !invalid {
			if preliminary && e.standardPreliminaryProse() {
				// The runtime knows it stopped at a source budget. Keep that
				// delivery status visible even when the model omits the caveat,
				// and bind the status to the same checked candidate and receipt.
				fullAnswer = withStandardPreliminaryNotice(ctx, fullAnswer)
			}
			if attempt == 2 {
				e.turnReview.NumericWarningAllowed = true
			}
			decision := e.checkCandidate(ctx, query, fullAnswer, llmResult.ReasoningContent, state, conversation, true)
			fullAnswer = decision.Candidate
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if decision.Err != nil {
				logger.Warnf(ctx, "[Agent][FinalAnswer] Candidate check failed: %v", decision.Err)
				fullAnswer = answerReviewUnavailableFallback
				truncated = false
				break
			}
			unsupported = decision.Unsupported
			invalid = decision.Disposition != candidateAccepted && decision.Disposition != candidateNumericWarning
			if decision.Disposition == candidateNumericWarning {
				if delivery, ok := e.numericWarningDelivery(fullAnswer, state); ok {
					fullAnswer = delivery
				} else {
					invalid = true
				}
			}
		}
		if !invalid {
			outcome = "answer"
			if preliminary {
				outcome = "partial"
			}
			break
		}

		if attempt == 2 || !e.spendCandidateRepair() {
			fullAnswer = incompleteAnswerFallback
			if len(unsupported) > 0 {
				fullAnswer = finalNumericFailureFallback
			}
			truncated = false
			break
		}
		if !reviewedDelivery && emptyFinalDelivery(llmResult) {
			// Compatible endpoints can consume generated tool-call markup while
			// tools are disabled and report a natural stop with billed tokens but
			// no message payload. Use the existing answer delivery contract for
			// the next budgeted repair; decoding still precedes all source checks.
			// This neither enables thinking nor adds a model call or repair slot.
			reviewedDelivery = true
			messages = withReviewedAnswerDeliveryInstruction(messages)
			common.PipelineInfo(ctx, "Agent", "empty_final_delivery_recovery", map[string]interface{}{
				"attempt":           attempt + 2,
				"completion_tokens": llmResult.Usage.CompletionTokens,
				"delivery":          "reviewed_answer",
			})
		}
		// Keep the actual rejected candidate alongside feedback for targeted repair.
		messages = append(messages, chat.Message{Role: "assistant", Content: fullAnswer})
		messages = append(messages, chat.Message{Role: "system", Content: "内部运行时反馈：上次回答残缺或含未通过依据检查的数字。请依据已取得的真实来源，直接简短完整回答原问题；非必要的附加数字直接删除，不为保留它们扩展查询。缺失证据应说明，不能从草稿补造数值。字段名称、规格代码和原公式须完整引用工具原文，不用简称，不省略公式参数或单元格行号；身份代码不能当成统计值。未核验数字：" + strings.Join(unsupported, "、") + e.repeatedNumericRepairAdvice() + e.answerRepairAdvice()})
	}
	if outcome != "answer" && e.collaboration != nil && e.collaboration.Child != nil {
		return fmt.Errorf("delegated final answer could not be verified")
	}
	e.commitTerminalResult(ctx, state, sessionID, fullAnswer, "", outcome, truncated)
	logger.Infof(ctx, "[Agent][FinalAnswer] Final answer generated: %d characters", len(fullAnswer))
	common.PipelineInfo(ctx, "Agent", "final_answer_done", map[string]interface{}{
		"session_id": sessionID,
		"answer_len": len(fullAnswer),
	})
	return nil
}

// Distinguish an empty provider payload from a malformed answer, a truncated
// stream, reasoning-only output, or an actual tool request. Those keep their
// existing recovery and validation paths.
func emptyFinalDelivery(result *streamLLMResult) bool {
	return result != nil && strings.TrimSpace(result.Content) == "" &&
		strings.TrimSpace(result.ReasoningContent) == "" && result.StreamError == "" &&
		len(result.ToolCalls) == 0 && isNaturalStopFinishReason(result.FinishReason) &&
		result.Usage != nil && result.Usage.CompletionTokens > 0
}

// Measure the same handle-encoded conversation sent by streamLLMToEventBus,
// plus the runtime notice appended by the protocol adapter.
func (e *AgentEngine) finalAnswerInputTokens(messages []chat.Message, notice string) int {
	return e.tokenEstimator.EstimateMessages(e.modelContext.EncodeMessages(messages)) +
		e.tokenEstimator.EstimateMessage(&chat.Message{Role: "system", Content: notice})
}

func (e *AgentEngine) finalAnswerInputBudget() int {
	return max(e.config.MaxContextTokens-e.getCompletionTokenBudget()-contextSafetyTokens, 1)
}

func (e *AgentEngine) finalAnswerNeedsCompaction(messages []chat.Message, notice string) bool {
	return e.config.MaxContextTokens > 0 && e.finalAnswerInputTokens(messages, notice) > e.finalAnswerInputBudget()
}

// Reuse normal compaction and checkpoint persistence. A last tool result can
// exceed the entire window on its own; after summarizing older history, trim
// it to the space left by the fixed prompt, notice and completion budget.
func (e *AgentEngine) compactFinalAnswerContext(
	ctx context.Context, messages []chat.Message, round int, notice string, reason compaction.Reason,
) ([]chat.Message, bool) {
	if ctx.Err() != nil || e.config.MaxContextTokens <= 0 {
		return messages, false
	}
	before := e.finalAnswerInputTokens(messages, notice)
	compacted, changed := e.runCompaction(ctx, messages, round, reason)
	if ctx.Err() != nil {
		return messages, false
	}
	inputTokens := e.finalAnswerInputTokens(compacted, notice)
	if inputTokens > e.finalAnswerInputBudget() || (reason == compaction.ReasonOverflow && !changed) {
		toolTokens := 0
		for _, msg := range e.modelContext.EncodeMessages(compacted) {
			if msg.Role == "tool" {
				toolTokens += e.tokenEstimator.EstimateMessage(&msg)
			}
		}
		budget := min(toolResultBudget(e.config.MaxContextTokens),
			max(e.finalAnswerInputBudget()-(inputTokens-toolTokens), 1))
		var trimmed bool
		compacted, trimmed = trimToolResultsToBudget(compacted, e.tokenEstimator, budget)
		if trimmed {
			e.contextRewrites++
			logger.Infof(ctx, "[Agent][FinalAnswer] Trimmed tool results to %d tokens", budget)
		}
	}
	return compacted, e.finalAnswerInputTokens(compacted, notice) < before
}

// handleMaxIterations generates a final answer when the agent loop exhausted all iterations
// without the LLM producing a natural stop. Completion still persists the
// fallback and prior steps on failure, while the error remains observable.
func (e *AgentEngine) handleMaxIterations(
	ctx context.Context, query string, state *types.AgentState, sessionID string, messages []chat.Message,
) error {
	logger.Info(ctx, "Reached max iterations, generating final answer")
	common.PipelineWarn(ctx, "Agent", "max_iterations_reached", map[string]interface{}{
		"iterations": state.CurrentRound,
		"max":        e.config.MaxIterations,
	})

	// Stream final answer generation through EventBus
	if err := e.streamFinalAnswerToEventBus(ctx, query, state, sessionID, messages); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logger.Errorf(ctx, "Failed to synthesize final answer: %v", err)
		common.PipelineError(ctx, "Agent", "final_answer_failed", map[string]interface{}{
			"error": err.Error(),
		})
		e.commitTerminalResult(ctx, state, sessionID, "Sorry, I was unable to generate a complete answer.", "", "failed", false)
		return fmt.Errorf("failed to synthesize final answer after max iterations: %w", err)
	}
	state.IsComplete = true
	return nil
}

// emitCompletionEvent emits the EventAgentComplete event with execution summary.
func (e *AgentEngine) emitCompletionEvent(
	ctx context.Context, state *types.AgentState, sessionID, messageID string, startTime time.Time,
) {
	steps := state.RoundSteps
	if state.SuspendedStep != nil {
		steps = append(append([]types.AgentStep(nil), steps...), *state.SuspendedStep)
	}
	seen := e.takeBackgroundSeen()
	if len(state.PendingSteerMessages) > 0 || len(state.PendingBackground) > 0 || len(seen) > 0 {
		// A stop or model failure can arrive after delivery but before the next
		// response exists. Preserve that boundary without inventing an answer.
		steps = append(append([]types.AgentStep(nil), steps...), types.AgentStep{
			Iteration:            state.CurrentRound,
			UserMessagesBefore:   state.PendingSteerMessages,
			BackgroundInjections: state.PendingBackground,
			BackgroundSeenIDs:    seen,
		})
	}
	// Convert knowledge refs to interface{} slice for event data
	e.publishFinalKnowledgeReferences(ctx, state, sessionID)
	knowledgeRefs := state.KnowledgeRefs
	if !e.config.CitationsEnabled() {
		// A resumed checkpoint keeps its durable references, while this run's
		// output still respects the current citation setting.
		knowledgeRefs = nil
	}
	knowledgeRefsInterface := make([]interface{}, 0, len(knowledgeRefs))
	for _, ref := range knowledgeRefs {
		knowledgeRefsInterface = append(knowledgeRefsInterface, ref)
	}
	evidenceRefs := make([]interface{}, 0, len(state.EvidenceRefs))
	for _, ref := range state.EvidenceRefs {
		evidenceRefs = append(evidenceRefs, ref)
	}

	data := event.AgentCompleteData{
		Artifacts:       e.collaborationArtifacts(),
		EvidenceRefs:    evidenceRefs,
		FinalAnswer:     state.FinalAnswer,
		KnowledgeRefs:   knowledgeRefsInterface,
		AgentSteps:      steps,
		Usage:           turnUsage(state),
		TotalSteps:      len(state.RoundSteps),
		TotalDurationMs: time.Since(startTime).Milliseconds(),
		MessageID:       messageID, // Include message ID for proper message update
	}
	// The turn's usage is aggregated only from calls made through this chat
	// model (each round and the final answer), so it is the model the usage
	// belongs to. The message row keeps the requested binding, which can differ
	// when a request override falls back to the agent's model.
	if e.chatModel != nil {
		data.ModelID = e.chatModel.GetModelID()
	}
	if state.Suspended != nil {
		data.Suspended = state.Suspended
	}
	e.eventBus.Emit(ctx, event.Event{
		ID:        generateEventID("complete"),
		Type:      event.EventAgentComplete,
		SessionID: sessionID,
		Data:      data,
	})

	logger.Infof(ctx, "Agent execution completed in %d rounds", state.CurrentRound)
}

// turnUsage returns the turn's aggregated LLM usage, or nil when no round
// reported usage so the field stays absent from the completion event and the
// persisted message alike.
func turnUsage(state *types.AgentState) *types.TokenUsage {
	// A token scale is worth persisting on its own: the next turn's history
	// loading is calibrated by it even when this provider reported no total.
	if state == nil || (state.TurnUsage.TotalTokens == 0 && state.TurnUsage.ContextTokenScale <= 0) {
		return nil
	}
	usage := state.TurnUsage
	return &usage
}
