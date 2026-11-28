package agent

import (
	"context"
	"fmt"
	"slices"
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

type finalAnswerOptions struct {
	preliminary        bool
	stopReason         string
	assistantMessageID string
	chartAnswer        *boundedChartAnswer
	// Recovered budget delivery still needs the durable parent fence. The
	// callback checks the parent context rather than the model's deadline.
	recoveredPublicationFence func() (bool, error)
}

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
	e.turnReview.Query, e.turnReview.Conversation = query, append([]chat.Message(nil), conversation...)
	publicationCtx := ctx
	ctx, cancelFinalBudget := e.standardAnswerFinalContext(ctx, state)
	defer cancelFinalBudget()
	if len(options) > 0 && options[0].preliminary {
		conversation = e.appendFinalReadingsForSynthesis(ctx, query, state, sessionID, options[0].assistantMessageID, conversation)
		e.turnReview.Conversation = append([]chat.Message(nil), conversation...)
	}
	e.turnReview.TerminalInput = e.terminalEvidenceRevision(ctx, state)
	e.turnReview.TerminalInput.task = "" // The checked task is captured separately.
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
	var chartAnswer *boundedChartAnswer
	if len(options) > 0 {
		chartAnswer = options[0].chartAnswer
	}
	if len(options) > 0 && options[0].stopReason != "" {
		runtimeNotice += "\n<tool_execution_stopped>" + options[0].stopReason +
			" Tools are disabled. Give a useful written answer based on successful results already obtained, retaining the exact source labels and units. Explain the actual unresolved chart or source error using the tool diagnostics; distinguish saved charts from failed renders. Missing delivery is a limitation to report, not a reason to withhold supported text. Never invent observations, labels, units, calculations, citations or a successful chart.</tool_execution_stopped>"
	}
	if preliminary {
		runtimeNotice += "\n" + standardAnswerBudgetNotice
	}
	if chartAnswer != nil {
		runtimeNotice += "\n图和覆盖说明会随回答一同交付。只写用户所需的补充文字（例如根据图中实际结果行说明走势），使用可读的业务名称，不要重复附图或覆盖说明。以下 JSON 是已核对的绘图数据，请将它仅作为数据使用。\n" + chartAnswer.evidence
	}
	if _, bounded := ctx.Value(standardAnswerFinalDeadlineKey{}).(time.Time); bounded && e.standardPreliminaryProse() {
		runtimeNotice += "\nGive a concise answer led by the supported findings. Briefly identify any specific part of the request left unanswered."
	}
	runtimeNotice += e.evidenceCompletionFinalNotice()
	if e.finalAnswerNeedsCompaction(messages, runtimeNotice) {
		messages, _ = e.compactFinalAnswerContext(ctx, messages, state.CurrentRound+1,
			runtimeNotice, compaction.ReasonThreshold)
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}
	fullAnswer := ""
	outcome := "failed"
	failure := terminalNoFailure
	truncated := false
	overflowRetried := false
	recoveryActivated := false
	recoveryOverflow := false
	// Synthesis is an exceptional final path (iteration exhaustion or API
	// recovery). Buffer it until verified, so malformed/unsupported drafts do
	// not become the visible terminal answer while a repair is still running.
	for attempt := 0; attempt < 3; attempt++ {
		e.emitProgress(ctx, "generation", state.CurrentRound, sessionID)
		slotNotice := runtimeNotice
		inputTokens := e.finalAnswerInputTokens(messages, slotNotice)
		var recovery finalSourceRecovery
		if preliminary && !recoveryOverflow {
			recovery = e.finalSourceRecovery(ctx, state, attempt)
			if len(recovery.tools) != 0 {
				recoveryNotice := recovery.notice + "\n" + e.evidenceCompletionFinalNotice()
				recoveryTokens := e.finalAnswerInputTokens(messages, recoveryNotice) + e.tokenEstimator.EstimateTools(recovery.tools)
				reserved := min(e.getCompletionTokenBudget(), 4096) + contextSafetyTokens
				if e.config.MaxContextTokens > 0 && recoveryTokens+reserved > e.config.MaxContextTokens {
					recovery = finalSourceRecovery{}
				} else {
					inputTokens, slotNotice, recoveryActivated = recoveryTokens, recoveryNotice, true
				}
			}
		}
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
			RuntimeNotice: slotNotice,
			Temperature:   e.config.Temperature, MaxCompletionTokens: budget,
			PromptCacheKey: sessionID, ToolChoice: "none", Thinking: &disabled,
			ReasoningEffort: reasoning, ThinkingBudgetTokens: 0,
		}
		if len(recovery.tools) != 0 {
			parallel := true
			opts.Tools, opts.ToolChoice, opts.ParallelToolCalls = recovery.tools, "auto", &parallel
		}
		if reviewedDelivery && len(recovery.tools) == 0 {
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
		if overflow && recoveryActivated {
			// The recovery slot is spent. The original remaining prose
			// attempts can use their own window without a new compaction call.
			recoveryOverflow = true
			fullAnswer, failure, truncated = "", terminalBudget, false
			continue
		}
		if overflow && !overflowRetried && chartAnswer == nil {
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
		if len(recovery.tools) != 0 && llmResult != nil && len(llmResult.ToolCalls) != 0 {
			step, recoveryErr := e.runFinalSourceRecovery(ctx, recovery, llmResult, state, sessionID, options[0].assistantMessageID, query, messages)
			if recoveryErr != nil {
				return recoveryErr
			}
			if step != nil {
				messages = e.appendToolResults(messages, *step)
				conversation = e.appendToolResults(conversation, *step)
				e.refreshEvidenceCompletion(ctx, query, state, conversation)
			}
			// An invalid batch supplies no observations and still spends
			// this attempt. The last attempt always remains prose-only.
			continue
		}
		fullAnswer = agenttools.StripThinkBlocks(llmResult.Content)
		if isReviewedAnswerFormat(opts.Format) {
			fullAnswer = agenttools.StripThinkBlocks(decodeReviewedAnswer(cleanReviewedAnswerEnvelope(llmResult.Content)))
			fullAnswer = e.modelContext.DecodeOutputText(fullAnswer)
		}
		fullAnswer = e.acceptCoverageAnswer(ctx, query, state, conversation, fullAnswer,
			llmResult.StreamError == "" && len(llmResult.ToolCalls) == 0 && isNaturalStopFinishReason(llmResult.FinishReason))
		truncated = isLengthFinishReason(llmResult.FinishReason)
		// A provider can ignore tool_choice=none. Never accept its pre-tool
		// progress text as the final answer, or silently drop requested calls.
		invalid := strings.TrimSpace(fullAnswer) == "" || llmResult.StreamError != "" || len(llmResult.ToolCalls) > 0 || !isNaturalStopFinishReason(llmResult.FinishReason)
		unsupported := []string{}
		var checked candidateDecision
		if !invalid {
			if chartAnswer != nil {
				if images, valid := markdownImageResources(fullAnswer); !valid || len(images) > 0 {
					return fmt.Errorf("final synthesis included an unexpected image")
				}
				fullAnswer = chartAnswer.markdown + "\n\n" + strings.TrimSpace(fullAnswer)
			}
			if attempt == 2 {
				e.turnReview.NumericWarningAllowed = true
			}
			checkCtx := ctx
			if recoveryActivated {
				checkCtx = context.WithValue(ctx, finalSourceRecoveryKey{}, true)
			}
			decision := e.checkCandidate(checkCtx, query, fullAnswer, llmResult.ReasoningContent, state, conversation, true)
			checked = decision
			fullAnswer = decision.Candidate
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if decision.Err != nil {
				logger.Warnf(ctx, "[Agent][FinalAnswer] Candidate check failed: %v", decision.Err)
				fullAnswer, failure = "", terminalReview
				truncated = false
				break
			}
			unsupported = decision.Unsupported
			invalid = decision.Disposition != candidateAccepted && decision.Disposition != candidateNumericWarning &&
				decision.Disposition != candidateEvidencePartial
			if decision.Disposition == candidateNumericWarning {
				if delivery, ok := e.numericWarningDelivery(fullAnswer, state); ok {
					fullAnswer = delivery
				} else {
					invalid = true
				}
			}
			if chartAnswer != nil {
				invalid = decision.Disposition != candidateAccepted || !strings.HasPrefix(fullAnswer, chartAnswer.markdown)
			}
		}
		if !invalid {
			outcome = "answer"
			if preliminary || checked.Disposition == candidateEvidencePartial {
				outcome = "partial"
			}
			break
		}
		if chartAnswer != nil {
			break
		}
		if checked.ReasonCode == string(terminalAnswerEvidence) && e.currentCoverageDraftReview(ctx, state).directory &&
			(!types.VerifiedPublicationEnabled() || len(recovery.tools) == 0) {
			// The available native receipts are directories. A text-only
			// revision cannot obtain the missing underlying observations.
			fullAnswer, failure, truncated = "", terminalAnswerEvidence, false
			break
		}

		if attempt == 2 || !e.spendCandidateRepair() {
			fullAnswer, failure = "", terminalIncomplete
			if len(unsupported) > 0 {
				failure = terminalNumeric
			} else if truncated {
				failure = terminalTruncated
			} else if emptyFinalDelivery(llmResult) || strings.TrimSpace(llmResult.Content) == "" {
				failure = terminalEmpty
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
		if preliminary && !recoveryActivated {
			var readStep *types.AgentStep
			readStep, err = e.finalCoverageRead(ctx, query, state, sessionID, options[0].assistantMessageID, messages, checked)
			if err != nil {
				return err
			}
			if readStep != nil {
				messages = e.appendToolResults(messages, *readStep)
				// Keep actual execution in the source-check transcript too, without
				// promoting the advisory recovery feedback into task or evidence.
				conversation = e.appendToolResults(conversation, *readStep)
			}
		}
		// Keep the actual rejected candidate alongside feedback for targeted repair.
		messages = append(messages, chat.Message{Role: "assistant", Content: fullAnswer})
		messages = append(messages, chat.Message{Role: "system", Content: "内部运行时反馈：上次回答残缺或含未通过依据检查的数字。请依据已取得的真实来源，直接简短完整回答原问题；非必要的附加数字直接删除，不为保留它们扩展查询。缺失证据应说明，不能从草稿补造数值。字段名称、规格代码和原公式须完整引用工具原文，不用简称，不省略公式参数或单元格行号；身份代码不能当成统计值。未核验数字：" + strings.Join(unsupported, "、") + e.repeatedNumericRepairAdvice() + e.answerRepairAdvice()})
	}
	if chartAnswer != nil {
		if outcome != "answer" {
			return fmt.Errorf("final chart analysis could not be verified")
		}
		if err := e.executionLeaseError(ctx); err != nil {
			return err
		}
	}
	if outcome != "answer" && e.collaboration != nil && e.collaboration.Child != nil {
		return fmt.Errorf("delegated final answer could not be verified")
	}
	if len(options) > 0 && options[0].recoveredPublicationFence != nil {
		recovered, err := options[0].recoveredPublicationFence()
		if err != nil {
			return err
		}
		if (recovered || failure != terminalNoFailure || terminalCandidateFallback(fullAnswer)) &&
			e.evidenceCompletionEligible() && e.standardTextDelivery(state) {
			failure = terminalCollaborationContention
		}
		ctx = publicationCtx // Only model work is subject to the synthesis deadline.
	}
	e.commitTerminalResult(ctx, state, sessionID, fullAnswer, "", outcome, truncated, failure)
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
	ctx context.Context, query string, state *types.AgentState, sessionID string, messages []chat.Message, chartAnswer ...*boundedChartAnswer,
) error {
	logger.Info(ctx, "Reached max iterations, generating final answer")
	common.PipelineWarn(ctx, "Agent", "max_iterations_reached", map[string]interface{}{
		"iterations": state.CurrentRound,
		"max":        e.config.MaxIterations,
	})

	return e.finishBoundedResultAnswer(ctx, query, state, sessionID, messages, "The iteration budget has been exhausted.", toolStopSynthesisBudget, terminalBudget, chartAnswer...)
}

// emitCompletionEvent emits the EventAgentComplete event with execution summary.
func (e *AgentEngine) emitCompletionEvent(
	ctx context.Context, state *types.AgentState, sessionID, messageID string, startTime time.Time,
) {
	if e.ParentPublicationInterrupted(ctx) {
		return
	}
	if state.IsComplete && state.Suspended == nil {
		e.finalizeTerminalPublication(ctx, state, sessionID)
	}
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
	if !state.IsComplete || state.Suspended != nil {
		state.FinalAnswer = stripCoverageAnswer(state.FinalAnswer)
		if types.VerifiedPublicationEnabled() {
			state.FinalAnswer = types.StripAgentAnswerMarkup(state.FinalAnswer)
		}
		if terminalCandidateFallback(state.FinalAnswer) {
			state.FinalAnswer = ""
		}
	}
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
	if state.IsComplete && state.Suspended == nil && e.turnReview.TerminalReceipt.notice != "" {
		receipt := e.turnReview.TerminalReceipt
		data.Incomplete = &event.AgentIncompleteData{Reason: string(receipt.failure), Notice: receipt.notice}
		if types.VerifiedPublicationEnabled() && receipt.partial && state.FinalAnswer != "" {
			data.Incomplete.PartialAnswer, data.Incomplete.Notice = true, state.FinalAnswer
		} else if types.VerifiedPublicationEnabled() {
			data.Incomplete.Notice = types.StripAgentAnswerMarkup(receipt.notice)
		}
	}
	e.eventBus.Emit(ctx, event.Event{
		ID:        generateEventID("complete"),
		Type:      event.EventAgentComplete,
		SessionID: sessionID,
		Data:      data,
	})

	logger.Infof(ctx, "Agent execution completed in %d rounds", state.CurrentRound)
}

func (e *AgentEngine) finalizeTerminalPublication(ctx context.Context, state *types.AgentState, sessionID string) {
	state.FinalAnswer = stripCoverageAnswer(state.FinalAnswer)
	priorAnswer := state.FinalAnswer
	receipt := &e.turnReview.TerminalReceipt
	finalRead := e.finalReadPublicationEligible(state)
	if finalRead && !e.turnReview.Committed {
		working := *state
		working.IsComplete = false
		if e.recoverFinalReadings(ctx, &working, sessionID) {
			state.RoundSteps, state.CurrentRound = working.RoundSteps, working.CurrentRound
		}
	}
	revision := e.terminalEvidenceRevision(ctx, state)
	failure := terminalNoFailure
	if !receipt.evidence.current(revision) {
		failure = terminalSourceChanged
	} else if receipt.notice == "" && terminalCandidateFallback(state.FinalAnswer) {
		failure = legacyTerminalFailure(state.FinalAnswer)
	} else if receipt.answerHash != "" && receipt.answerHash != candidateTextDigest(state.FinalAnswer) && e.turnReview.UnverifiedCitationDelivery == nil {
		failure = terminalReview
	} else if receipt.failure != terminalNoFailure && len(revision.calls) > len(receipt.evidence.calls) {
		// Add newly completed receipts to a fallback without rewriting a model draft.
		failure = receipt.failure
	} else if receipt.failure == terminalNoFailure && receipt.answerHash == "" {
		failure = e.terminalCandidateEvidenceFailure(ctx, state, state.FinalAnswer)
	}
	selectReadings := finalRead && (failure != terminalNoFailure || !e.turnReview.Committed || len(revision.calls) > len(receipt.evidence.calls))
	qualifiedReferences := false
	if failure == terminalNoFailure && receipt.failure == terminalNoFailure && !receipt.fromEvidence {
		qualifiedReferences = e.publishFinalKnowledgeReferences(ctx, state, sessionID)
		state.FinalAnswer = stripCoverageAnswer(state.FinalAnswer)
		if revision.scope != e.sourceScopeDigest(ctx, false) {
			revision = e.terminalEvidenceRevision(ctx, state)
			failure = terminalSourceChanged
		} else if terminalCandidateFallback(state.FinalAnswer) {
			failure = legacyTerminalFailure(state.FinalAnswer)
		} else if finalRead && terminalCandidateFallback(types.StripAgentAnswerMarkup(state.FinalAnswer)) {
			failure = terminalEmpty
		}
		selectReadings = selectReadings || finalRead && failure != terminalNoFailure
	}
	stale := receipt.evidence.staleCalls(revision)
	for id := range receipt.staleCalls {
		if stale == nil {
			stale = make(map[string]bool)
		}
		stale[id] = true
	}
	if selectReadings {
		current := terminalCurrentState(state, stale)
		result := e.selectFinalReadPublication(ctx, current, state.FinalAnswer, failure, "answer")
		if result.fromEvidence {
			state.FinalAnswer, receipt.notice = result.answer, result.notice
			receipt.partial, receipt.fromEvidence = result.partial, true
			state.EvidenceRefs, state.KnowledgeRefs = nil, nil
			e.turnReview.TerminalTruncated = false
			receipt.failure = failure
			if result.complete {
				receipt.failure = terminalNoFailure
			} else if failure == terminalNoFailure && result.notice != "" {
				receipt.failure = terminalAnswerEvidence
			}
			qualifiedReferences = false
		}
	} else if failure != terminalNoFailure {
		current := terminalCurrentState(state, stale)
		result := e.terminalCoveragePublication(ctx, current, failure)
		state.FinalAnswer, receipt.notice = result.answer, result.notice
		if failure != terminalSourceChanged {
			state.FinalAnswer = e.retainReadSupplement(ctx, current, state.FinalAnswer)
		}
		receipt.partial = result.partial
		state.EvidenceRefs, state.KnowledgeRefs = nil, nil
		e.turnReview.TerminalTruncated = false
		receipt.failure = failure
		receipt.fromEvidence = result.answer != ""
		if result.complete {
			receipt.failure = terminalNoFailure
		}
		qualifiedReferences = false
	}
	if receipt.fromEvidence && state.FinalAnswer != "" && e.evidenceCompletionEligible() {
		if types.VerifiedPublicationEnabled() {
			current := terminalCurrentState(state, stale)
			state.KnowledgeRefs = e.currentCandidateKnowledgeReferences(state.FinalAnswer, current, e.turnReview.Conversation)
			state.EvidenceRefs = e.currentCandidateEvidenceReferences(state.FinalAnswer, current, ctx)
		} else {
			state.KnowledgeRefs = e.currentCandidateKnowledgeReferences(state.FinalAnswer, state, e.turnReview.Conversation)
		}
		qualifiedReferences = true
	}
	if len(stale) != 0 {
		withholdStaleTerminalTrace(state, stale, priorAnswer, firstNonEmptyAgent(e.turnReview.Last.Candidate, receipt.submitted))
		for id := range stale {
			delete(revision.calls, id)
		}
		receipt.staleCalls = nil
	}
	if types.VerifiedPublicationEnabled() {
		if qualifiedReferences {
			state.FinalAnswer, state.EvidenceRefs = types.ProjectAgentAnswer(state.FinalAnswer, state.KnowledgeRefs, state.EvidenceRefs, e.modelContext.CitableWebTitle)
		} else {
			state.FinalAnswer = types.StripAgentAnswerMarkup(state.FinalAnswer)
		}
		if qualifiedReferences {
			e.emitFinalAnswerReferences(ctx, state, sessionID)
		}
	}
	receipt.answerHash, receipt.evidence = candidateTextDigest(state.FinalAnswer), revision
	if e.turnReview.Committed || finalRead {
		e.turnReview.Committed = true
		e.publishTerminalStream(ctx, state, sessionID)
	}
	e.closeAnswerStream(ctx, sessionID, e.turnReview.TerminalStreamID, e.turnReview.TerminalTruncated)
}

func terminalCurrentState(state *types.AgentState, stale map[string]bool) *types.AgentState {
	if len(stale) == 0 {
		return state
	}
	copy := *state
	copy.RoundSteps = slices.Clone(state.RoundSteps)
	for index, step := range copy.RoundSteps {
		copy.RoundSteps[index].ToolCalls = slices.DeleteFunc(slices.Clone(step.ToolCalls), func(call types.ToolCall) bool {
			return stale[call.ID]
		})
	}
	return &copy
}

// Completed history is itself published and persisted. Withhold changed
// native receipts and the known dependent terminal draft.
func withholdStaleTerminalTrace(state *types.AgentState, stale map[string]bool, priorAnswer, checkedDraft string) {
	matches := func(text string) bool {
		text = strings.TrimSpace(stripCoverageAnswer(text))
		return text != "" && (text == strings.TrimSpace(priorAnswer) || text == strings.TrimSpace(checkedDraft))
	}
	for i := range state.RoundSteps {
		step := &state.RoundSteps[i]
		for j, call := range step.ToolCalls {
			if stale[call.ID] {
				step.ToolCalls[j] = types.ToolCall{ID: call.ID, Name: call.Name, Duration: call.Duration}
			}
		}
		if matches(step.Thought) || matches(step.Progress) {
			step.Thought, step.Progress, step.ReasoningContent, step.ReasoningSignature = "", "", "", ""
			step.ReasoningMetadata = nil
		}
	}
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
